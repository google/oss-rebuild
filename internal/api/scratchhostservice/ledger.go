// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"iter"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
)

// FrameSize is the byte target for each output frame.
const FrameSize = 64 << 10

// ExecStatus is one op's state. Its JSON is the exec API's wire contract.
type ExecStatus struct {
	Done         bool      `json:"done"`
	ExitCode     int       `json:"exit_code,omitempty"`
	TimedOut     bool      `json:"timed_out,omitempty"`
	Killed       bool      `json:"killed,omitempty"`
	OutputCapped bool      `json:"output_capped,omitempty"` // Killed at the output cap.
	TotalBytes   int64     `json:"total_bytes"`             // Output bytes written. Monotonic.
	StartedAt    time.Time `json:"started_at,omitzero"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	ErrMsg       string    `json:"err_msg,omitempty"` // Run error. If set, Done is true.
}

// OutputFrame is one chunk of an op's merged output, in offset order.
type OutputFrame struct {
	Offset  int64  `json:"offset"`
	Content []byte `json:"content"` // NOTE: []byte keeps JSON binary safe
}

// Runner runs one op's command, writing merged output to out, with
// sshChannel.Run's (code, err) contract. Kill and the deadline arrive
// through ctx.
type Runner func(ctx context.Context, out io.Writer) (exitCode int, err error)

// entry is one op's record.
type entry struct {
	startedAt  time.Time
	finishedAt time.Time
	deadline   time.Time          // bounds the run and anchors retention
	file       *os.File           // merged output, closed by gc
	cancel     context.CancelFunc // kills the run
	totalBytes int64              // set by finish
	done       bool
	exitCode   int
	timedOut   bool
	killed     bool
	capped     bool
	errMsg     string
}

// Ledger tracks one session's execs: op state, a capture file per op,
// deadlines and kill. Callers supply a Runner for how a command runs.
// Nothing survives the process, like the guests that run the ops.
type Ledger struct {
	dir       string
	maxOutput int64

	mu      sync.Mutex
	entries map[string]*entry
}

// NewLedger returns an empty Ledger keeping capture files in dir, which
// Discard removes, so it must be the ledger's own. maxOutput caps each op's
// capture, killing the op past it. Zero is unbounded.
func NewLedger(dir string, maxOutput int64) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.Wrap(err, "exec ledger dir")
	}
	return &Ledger{dir: dir, maxOutput: maxOutput, entries: map[string]*entry{}}, nil
}

// Start runs opID under a deadline of timeoutSeconds, which must be
// positive. It fails only if the op cannot be registered. Status reports
// the rest.
func (l *Ledger) Start(opID string, timeoutSeconds int, run Runner) error {
	f, err := os.CreateTemp(l.dir, "exec-out-*")
	if err != nil {
		return errors.Wrap(err, "open temp file")
	}
	// The run outlives this call and any request: only the deadline and Kill
	// end it. gc removes the file once retention lapses.
	started := time.Now().UTC()
	timeout := time.Duration(timeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	e := &entry{startedAt: started, deadline: started.Add(timeout), file: f, cancel: cancel}
	l.mu.Lock()
	if _, dup := l.entries[opID]; dup {
		l.mu.Unlock()
		cancel()
		_ = f.Close()
		_ = os.Remove(f.Name())
		return errors.Errorf("op %q already started", opID)
	}
	l.gc(started)
	l.entries[opID] = e
	l.mu.Unlock()
	var out io.Writer = f
	var capped *capWriter
	if l.maxOutput > 0 {
		// Past the cap the output is waste: the agent reads at most the last
		// 100,000 bytes. Cancelling ctx kills the run like the deadline.
		capped = &capWriter{w: f, limit: l.maxOutput, onCap: cancel}
		out = capped
	}
	go func() {
		defer cancel()
		code, runErr := run(ctx, out)
		if capped != nil && capped.tripped.Load() {
			// The Runner sees the cap kill as a cancellation. Record it as the cap.
			runErr = errors.Wrapf(errOutputCapped, "output exceeded %d bytes", l.maxOutput)
		}
		l.finish(opID, code, runErr)
	}()
	return nil
}

// Kill cancels a running op. Killing a finished op succeeds and changes
// nothing, so a kill racing the command's exit is harmless.
func (l *Ledger) Kill(opID string) error {
	l.mu.Lock()
	e, ok := l.entries[opID]
	if ok && !e.done {
		e.killed = true
	}
	l.mu.Unlock()
	if !ok {
		return errors.Errorf("unknown op %q", opID)
	}
	e.cancel()
	return nil
}

// Status returns the op's state, or false if the op is unknown.
func (l *Ledger) Status(opID string) (ExecStatus, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[opID]
	if !ok {
		return ExecStatus{}, false
	}
	// A running op's size is the file's. Once done it is what finish recorded.
	total := e.totalBytes
	if !e.done {
		total = size(e.file)
	}
	return ExecStatus{
		Done:         e.done,
		ExitCode:     e.exitCode,
		TimedOut:     e.timedOut,
		Killed:       e.killed,
		OutputCapped: e.capped,
		TotalBytes:   total,
		StartedAt:    e.startedAt,
		FinishedAt:   e.finishedAt,
		ErrMsg:       e.errMsg,
	}, true
}

// Frames streams the op's output from offset in FrameSize chunks, up to the
// file's size on entry. An unknown op is a pre-stream error, which the
// streaming layer maps to a unary error response.
func (l *Ledger) Frames(ctx context.Context, opID string, offset int64) iter.Seq2[*OutputFrame, error] {
	return func(yield func(*OutputFrame, error) bool) {
		l.mu.Lock()
		e, ok := l.entries[opID]
		l.mu.Unlock()
		if !ok {
			yield(nil, errors.Errorf("unknown op %q", opID))
			return
		}
		info, err := e.file.Stat()
		if err != nil {
			yield(nil, err)
			return
		}
		end := info.Size()
		if offset >= end {
			return
		}
		buf := make([]byte, FrameSize)
		for pos := offset; pos < end; {
			if ctx.Err() != nil {
				yield(nil, ctx.Err())
				return
			}
			n := min(int64(FrameSize), end-pos)
			rd, err := e.file.ReadAt(buf[:n], pos)
			if rd > 0 {
				if !yield(&OutputFrame{Offset: pos, Content: append([]byte(nil), buf[:rd]...)}, nil) {
					return
				}
				pos += int64(rd)
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
	}
}

// Discard cancels every op and removes the ledger's files and directory.
func (l *Ledger) Discard() {
	l.mu.Lock()
	entries := l.entries
	l.entries = map[string]*entry{}
	l.mu.Unlock()
	for _, e := range entries {
		e.cancel()
		_ = e.file.Close()
	}
	_ = os.RemoveAll(l.dir)
}

func (l *Ledger) finish(opID string, exitCode int, runErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[opID]
	if !ok {
		return
	}
	e.done = true
	e.exitCode = exitCode
	e.timedOut = errors.Is(runErr, context.DeadlineExceeded)
	e.capped = errors.Is(runErr, errOutputCapped)
	// Timeouts, kills and caps have their own flags. ErrMsg means an infra
	// failure.
	if runErr != nil && !e.timedOut && !e.killed && !e.capped {
		e.errMsg = runErr.Error()
	}
	e.finishedAt = time.Now().UTC()
	e.totalBytes = size(e.file)
}

// gcGrace keeps finished ops past their deadline for late pulls, such as
// the reaper's.
const gcGrace = time.Hour

// gc drops done entries and their files once deadline + gcGrace has passed.
// Must hold mu.
func (l *Ledger) gc(now time.Time) {
	for opID, e := range l.entries {
		if e.done && now.After(e.deadline.Add(gcGrace)) {
			delete(l.entries, opID)
			_ = e.file.Close()
			_ = os.Remove(e.file.Name())
		}
	}
}

// errOutputCapped marks a run killed at the output cap.
var errOutputCapped = errors.New("output capped")

// capWriter passes the first limit bytes to w, then discards the rest and
// calls onCap once, on the first discarded byte. Discarding keeps the
// command from blocking on a full pipe while it is killed. tripped is atomic
// because a killed run can return before its last writes drain.
type capWriter struct {
	w       io.Writer
	limit   int64
	onCap   func()
	written int64
	tripped atomic.Bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	room := c.limit - c.written
	if room > 0 {
		n, err := c.w.Write(p[:min(int64(len(p)), room)])
		c.written += int64(n)
		if err != nil {
			return n, err
		}
	}
	if int64(len(p)) > room && !c.tripped.Swap(true) {
		c.onCap()
	}
	return len(p), nil
}

func size(f *os.File) int64 {
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}
