// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"iter"
	"os"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/pkg/errors"
)

// outputChunkSize is the byte target for each /exec/op/output frame.
const outputChunkSize = 64 << 10

// StartRequest is the body of POST exec/start. The broker mints OpID,
// stamps ScratchID, and forwards. The worker spawns the command
// asynchronously; the broker then pulls status + output via
// exec/op/status and exec/op/output.
type StartRequest struct {
	OpID           string            `form:"op_id,required"`
	ScratchID      string            `form:"scratch_id,required"`
	Cmd            []string          `form:"cmd,required"`
	Cwd            string            `form:"cwd"`
	Env            map[string]string `form:"env"`
	StdinB64       string            `form:"stdin_b64"`
	TimeoutSeconds int               `form:"timeout_seconds"`
}

// Validate implements act.Input.
func (r StartRequest) Validate() error {
	if r.OpID == "" {
		return errors.New("op_id is required")
	}
	if r.ScratchID == "" {
		return errors.New("scratch_id is required")
	}
	if len(r.Cmd) == 0 {
		return errors.New("cmd is required")
	}
	if r.TimeoutSeconds < 0 {
		return errors.New("timeout_seconds must be >= 0")
	}
	return nil
}

// StatusRequest is the body of POST exec/op/status.
type StatusRequest struct {
	ID string `form:"id,required"`
}

// Validate implements act.Input.
func (r StatusRequest) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	return nil
}

// OutputRequest is the input to stream bytes from the op's capture file.
// It streams bytes from the requested Offset up to the capture file's size at
// receipt of the request. Writes that land after entry are not included.
type OutputRequest struct {
	ID     string `form:"id,required"`
	Offset int64  `form:"offset"`
}

// Validate implements act.Input.
func (r OutputRequest) Validate() error {
	if r.ID == "" {
		return errors.New("id is required")
	}
	if r.Offset < 0 {
		return errors.New("offset must be >= 0")
	}
	return nil
}

// OutputFrame is one chunk of the merged stdout+stderr output. Frames are
// contiguous from the request's Offset; consumers can rely on the chunks
// arriving in increasing-Offset order.
type OutputFrame struct {
	Offset  int64  `json:"offset"`
	Content []byte `json:"content"`
}

// ExecStatus is the worker's in-memory view of one op. Returned by
// exec/op/status. The broker mirrors this into Firestore on sync.
type ExecStatus struct {
	Done       bool      `json:"done"`
	ExitCode   int       `json:"exit_code,omitempty"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	TotalBytes int64     `json:"total_bytes"` // Output bytes written. Monotonic.
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	ErrMsg     string    `json:"err_msg,omitempty"` // Run error. If set, Done is true.
}

// execEntry is the in-memory record the worker keeps for one op. The
// worker has no GCP credentials and writes nothing externally; the
// broker pulls Status and Output on its own cadence (agent-poll-driven
// + reaper sweep).
type execEntry struct {
	startedAt  time.Time
	finishedAt time.Time
	file       *os.File // merged stdout+stderr; lifetime = env lifetime
	totalBytes int64    // updated under mu after each runCommand returns
	done       bool
	exitCode   int
	timedOut   bool
	errMsg     string
}

// ExecStore tracks per-opID state in the worker process. Concurrent
// access from the spawn goroutine, the /status handler, and the
// /output handler is mutex-guarded. Exported so tests can construct
// one.
type ExecStore struct {
	mu      sync.Mutex
	entries map[string]*execEntry
}

// NewExecStore returns an empty ExecStore.
func NewExecStore() *ExecStore { return &ExecStore{entries: map[string]*execEntry{}} }

func (s *ExecStore) create(opID string, f *os.File, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[opID] = &execEntry{startedAt: started, file: f}
}

func (s *ExecStore) finish(opID string, exitCode int, timedOut bool, errMsg string, totalBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[opID]
	if !ok {
		return
	}
	e.done = true
	e.exitCode = exitCode
	e.timedOut = timedOut
	e.errMsg = errMsg
	e.finishedAt = time.Now().UTC()
	e.totalBytes = totalBytes
}

// Forget releases the entry and its temp file. Called when the broker
// has finalized the op via Firestore Update; the file is no longer
// needed. Best-effort; missing entry is not an error.
func (s *ExecStore) Forget(opID string) {
	s.mu.Lock()
	e, ok := s.entries[opID]
	delete(s.entries, opID)
	s.mu.Unlock()
	if ok {
		_ = e.file.Close()
		_ = os.Remove(e.file.Name())
	}
}

// status returns a snapshot of the op's state. Returns (ExecStatus{}, false)
// if the op is unknown.
func (s *ExecStore) status(opID string) (ExecStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[opID]
	if !ok {
		return ExecStatus{}, false
	}
	// Re-stat the file for the current size: writes from runCommand
	// happen via the kernel's fd, not through this struct.
	var total int64
	if info, err := e.file.Stat(); err == nil {
		total = info.Size()
	}
	if e.done {
		// Once done, the size is frozen at the value we recorded.
		total = e.totalBytes
	}
	return ExecStatus{
		Done:       e.done,
		ExitCode:   e.exitCode,
		TimedOut:   e.timedOut,
		TotalBytes: total,
		StartedAt:  e.startedAt,
		FinishedAt: e.finishedAt,
		ErrMsg:     e.errMsg,
	}, true
}

// outputFrom emits the captured stdout+stderr buffer for opID, starting
// at offset, in outputChunkSize-byte chunks. Snapshot at Stat time:
// bytes written after handler entry are excluded from this response and
// will be picked up on the next call.
func (s *ExecStore) outputFrom(ctx context.Context, opID string, offset int64) iter.Seq2[*OutputFrame, error] {
	return func(yield func(*OutputFrame, error) bool) {
		s.mu.Lock()
		e, ok := s.entries[opID]
		s.mu.Unlock()
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
		buf := make([]byte, outputChunkSize)
		pos := offset
		for pos < end {
			if ctx.Err() != nil {
				yield(nil, ctx.Err())
				return
			}
			n := outputChunkSize
			if remaining := end - pos; int64(n) > remaining {
				n = int(remaining)
			}
			rd, err := e.file.ReadAt(buf[:n], pos)
			if rd > 0 {
				frame := &OutputFrame{
					Offset:  pos,
					Content: append([]byte(nil), buf[:rd]...),
				}
				if !yield(frame, nil) {
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

type ExecDeps struct {
	Store *ExecStore
	// TempDir is where the captured output file is created.
	// Empty uses the OS default.
	TempDir string
	// Workdir is the default cwd when a request omits Cwd.
	Workdir string
}

// ExecStart registers the op and spawns the requested command asynchronously.
func ExecStart(_ context.Context, req StartRequest, deps *ExecDeps) (*act.NoOutput, error) {
	outF, err := openMergedTemp(deps.TempDir)
	if err != nil {
		return nil, errors.Wrap(err, "open temp file")
	}
	// NOTE: the file outlives this function. The broker can /output
	// pull from it any time after we return. ExecStore.Forget (called
	// after broker finalizes) is what closes + removes it.
	deps.Store.create(req.OpID, outF, time.Now().UTC())
	go finalizeExec(req, outF, deps)
	return &act.NoOutput{}, nil
}

func finalizeExec(req StartRequest, outF *os.File, deps *ExecDeps) {
	// Detached: Use a fresh background ctx so call cancellation
	// doesn't kill the spawned command.
	ctx := context.Background()

	cwd := req.Cwd
	if cwd == "" {
		cwd = deps.Workdir
	}

	stdin, err := decodeStdin(req.StdinB64)
	if err != nil {
		size, _ := outF.Stat()
		var totalBytes int64
		if size != nil {
			totalBytes = size.Size()
		}
		deps.Store.finish(req.OpID, 0, false, "decode stdin: "+err.Error(), totalBytes)
		return
	}

	// Merge stdout+stderr by passing the same *os.File to both. Go's
	// exec package serializes writes when Stdout and Stderr compare ==,
	// so writes from each stream are atomic within themselves.
	code, runErr := runCommand(ctx, runSpec{
		Cmd:            req.Cmd,
		Cwd:            cwd,
		Env:            req.Env,
		Stdin:          stdin,
		TimeoutSeconds: req.TimeoutSeconds,
	}, outF, outF)

	// Snapshot final size before announcing Done. Subsequent reads see
	// this value (frozen after Done).
	info, statErr := outF.Stat()
	var total int64
	if statErr == nil {
		total = info.Size()
	}
	// A timeout is fully described by exit 124 + the TimedOut flag; we
	// suppress the DeadlineExceeded error message to avoid double-reporting.
	timedOut := errors.Is(runErr, context.DeadlineExceeded)
	var msg string
	if runErr != nil && !timedOut {
		msg = errors.Wrap(runErr, "run").Error()
	}
	deps.Store.finish(req.OpID, code, timedOut, msg, total)
}

type StatusDeps struct {
	Store *ExecStore
}

// Status returns the current ExecStatus for an op.
func Status(_ context.Context, req StatusRequest, deps *StatusDeps) (*ExecStatus, error) {
	if st, ok := deps.Store.status(req.ID); ok {
		return &st, nil
	}
	return nil, errors.Errorf("unknown op %q", req.ID)
}

// OutputDeps wires the Output handler.
type OutputDeps struct {
	Store *ExecStore
}

// Output emits the captured output of an op as a sequence of OutputFrames
// starting at req.Offset. The end of the response is whatever the
// capture file was sized at when the handler stat'd it; bytes that land
// after entry are not included (callers fetch them on a subsequent call).
//
// Unknown opID is a pre-stream error (yielded as the first (nil, err)
// pair), which the streaming layer maps to a unary HTTP error response.
func Output(ctx context.Context, req OutputRequest, deps *OutputDeps) iter.Seq2[*OutputFrame, error] {
	return deps.Store.outputFrom(ctx, req.ID, req.Offset)
}

func decodeStdin(b64 string) (io.Reader, error) {
	if b64 == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

func openMergedTemp(dir string) (*os.File, error) {
	return os.CreateTemp(dir, "exec-out-*")
}
