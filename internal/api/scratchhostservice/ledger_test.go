// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"bytes"
	"context"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// echo is a Runner that writes payload and exits with code.
func echo(payload []byte, code int) Runner {
	return func(_ context.Context, out io.Writer) (int, error) {
		_, err := out.Write(payload)
		return code, err
	}
}

// blockUntilDone writes prefix, then waits for ctx like a real runner.
func blockUntilDone(prefix []byte) Runner {
	return func(ctx context.Context, out io.Writer) (int, error) {
		if _, err := out.Write(prefix); err != nil {
			return 0, err
		}
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			return 124, context.DeadlineExceeded
		}
		return 0, context.Canceled
	}
}

func newLedger(t *testing.T, dir string) *Ledger {
	t.Helper()
	l, err := NewLedger(dir, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

// waitDone returns the op's status once done. In a synctest bubble a
// finishable op is done once the bubble idles, and the sleep advances the
// clock past any test deadline.
func waitDone(t *testing.T, status func() (ExecStatus, bool)) ExecStatus {
	t.Helper()
	for range 2 {
		synctest.Wait()
		if st, ok := status(); ok && st.Done {
			return st
		}
		time.Sleep(24 * time.Hour)
	}
	t.Fatal("op never finished")
	return ExecStatus{}
}

// ledgerDone is waitDone over a ledger.
func ledgerDone(t *testing.T, l *Ledger, opID string) ExecStatus {
	t.Helper()
	return waitDone(t, func() (ExecStatus, bool) { return l.Status(opID) })
}

// collect drains frames into their content and offsets.
func collect(t *testing.T, frames iter.Seq2[*OutputFrame, error]) ([]byte, []int64, error) {
	t.Helper()
	var (
		content []byte
		offsets []int64
	)
	for f, err := range frames {
		if err != nil {
			return content, offsets, err
		}
		offsets = append(offsets, f.Offset)
		content = append(content, f.Content...)
	}
	return content, offsets, nil
}

func TestLedger_Start_RoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 60, echo([]byte("hello"), 3)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		st := ledgerDone(t, l, "op")
		if st.ExitCode != 3 || st.TimedOut || st.Killed || st.ErrMsg != "" {
			t.Errorf("status = %+v; want clean exit 3", st)
		}
		if st.TotalBytes != 5 {
			t.Errorf("TotalBytes = %d; want 5", st.TotalBytes)
		}
		got, offsets, err := collect(t, l.Frames(context.Background(), "op", 0))
		if err != nil {
			t.Fatalf("Frames: %v", err)
		}
		if string(got) != "hello" || len(offsets) != 1 || offsets[0] != 0 {
			t.Errorf("content = %q offsets = %v; want %q [0]", got, offsets, "hello")
		}
	})
}

func TestLedger_Start_BinaryOutputIsByteExact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Every byte value past the frame boundary: framing and 8-bit clean.
		unit := make([]byte, 256)
		for i := range unit {
			unit[i] = byte(i)
		}
		payload := bytes.Repeat(unit, (FrameSize*5/2)/len(unit))
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 60, echo(payload, 0)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ledgerDone(t, l, "op")
		got, offsets, err := collect(t, l.Frames(context.Background(), "op", 0))
		if err != nil {
			t.Fatalf("Frames: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("output differs from payload (got %d bytes, want %d)", len(got), len(payload))
		}
		if len(offsets) != 3 {
			t.Fatalf("frames = %d; want 3 for %d bytes at %d per frame", len(offsets), len(payload), FrameSize)
		}
		for i, o := range offsets {
			if want := int64(i) * FrameSize; o != want {
				t.Errorf("offsets[%d] = %d; want %d", i, o, want)
			}
		}
	})
}

func TestLedger_Frames_FromMidOffsetAndPastEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 60, echo([]byte("0123456789"), 0)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ledgerDone(t, l, "op")
		got, offsets, err := collect(t, l.Frames(context.Background(), "op", 4))
		if err != nil {
			t.Fatalf("Frames: %v", err)
		}
		if string(got) != "456789" || offsets[0] != 4 {
			t.Errorf("content = %q offsets = %v; want %q first offset 4", got, offsets, "456789")
		}
		got, offsets, err = collect(t, l.Frames(context.Background(), "op", 100))
		if err != nil {
			t.Fatalf("Frames past end: %v", err)
		}
		if len(got) != 0 || len(offsets) != 0 {
			t.Errorf("past-end read = %q %v; want empty", got, offsets)
		}
	})
}

func TestLedger_Frames_UnknownOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if _, _, err := collect(t, l.Frames(context.Background(), "missing", 0)); err == nil {
			t.Error("Frames(missing) = nil error; want a pre-stream error")
		}
		if _, ok := l.Status("missing"); ok {
			t.Error("Status(missing) ok; want unknown")
		}
	})
}

// flood writes chunk until ctx ends, like a command reading its own capture.
func flood(chunk []byte) Runner {
	return func(ctx context.Context, out io.Writer) (int, error) {
		for ctx.Err() == nil {
			if _, err := out.Write(chunk); err != nil {
				return 0, err
			}
		}
		return 0, ctx.Err()
	}
}

func TestLedger_Start_TerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxOutput int64
		timeout   int
		run       Runner
		kill      bool
		want      ExecStatus
		output    string
	}{
		{
			name:      "UnderOutputCap",
			maxOutput: 1 << 20,
			timeout:   60,
			run:       echo([]byte("hello"), 0),
			want:      ExecStatus{Done: true, TotalBytes: 5},
			output:    "hello",
		},
		{
			name:      "AtOutputCap",
			maxOutput: 5,
			timeout:   60,
			run:       echo([]byte("hello"), 0),
			want:      ExecStatus{Done: true, TotalBytes: 5},
			output:    "hello",
		},
		{
			// Generous deadline: the cap, not the timeout, must end this op.
			name:      "OutputCapped",
			maxOutput: 32 << 10,
			timeout:   3600,
			run:       flood([]byte("runaway\n")),
			want:      ExecStatus{Done: true, OutputCapped: true, TotalBytes: 32 << 10},
			output:    strings.Repeat("runaway\n", 4096),
		},
		{
			// The timeout message is dropped and partial output survives.
			name:    "DeadlineEnforced",
			timeout: 1,
			run:     blockUntilDone([]byte("before-timeout")),
			want:    ExecStatus{Done: true, ExitCode: 124, TimedOut: true, TotalBytes: 14},
			output:  "before-timeout",
		},
		{
			name:    "KillBeatsDeadline",
			timeout: 1,
			run:     blockUntilDone(nil),
			kill:    true,
			want:    ExecStatus{Done: true, Killed: true},
		},
		{
			name:    "RunnerError",
			timeout: 60,
			run:     func(context.Context, io.Writer) (int, error) { return 0, io.ErrUnexpectedEOF },
			want:    ExecStatus{Done: true, ErrMsg: "unexpected EOF"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, err := NewLedger(t.TempDir(), tc.maxOutput)
				if err != nil {
					t.Fatalf("NewLedger: %v", err)
				}
				if err := l.Start("op", tc.timeout, tc.run); err != nil {
					t.Fatalf("Start: %v", err)
				}
				if tc.kill {
					if err := l.Kill("op"); err != nil {
						t.Fatalf("Kill: %v", err)
					}
				}
				got := ledgerDone(t, l, "op")
				if diff := cmp.Diff(tc.want, got, cmpopts.IgnoreFields(ExecStatus{}, "StartedAt", "FinishedAt")); diff != "" {
					t.Errorf("status mismatch (-want +got):\n%s", diff)
				}
				output, _, err := collect(t, l.Frames(context.Background(), "op", 0))
				if err != nil {
					t.Fatalf("Frames: %v", err)
				}
				if string(output) != tc.output {
					t.Errorf("output = %d bytes %.40q; want %d bytes %.40q", len(output), output, len(tc.output), tc.output)
				}
			})
		})
	}
}

func TestLedger_Kill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 3600, blockUntilDone([]byte("started"))); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := l.Kill("op"); err != nil {
			t.Fatalf("Kill: %v", err)
		}
		st := ledgerDone(t, l, "op")
		if !st.Killed || st.TimedOut || st.ErrMsg != "" {
			t.Errorf("status = %+v; want Killed without TimedOut/ErrMsg", st)
		}
		// The captured output survives the kill.
		got, _, err := collect(t, l.Frames(context.Background(), "op", 0))
		if err != nil {
			t.Fatalf("Frames: %v", err)
		}
		if string(got) != "started" {
			t.Errorf("output = %q; want the bytes written before the kill", got)
		}
		// Killing a finished op is a no-op.
		if err := l.Kill("op"); err != nil {
			t.Errorf("second Kill: %v", err)
		}
		if err := l.Kill("missing"); err == nil {
			t.Error("Kill(missing) = nil; want unknown-op error")
		}
	})
}

func TestLedger_Kill_FinishedOpIsUntouched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 60, echo([]byte("hi"), 0)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ledgerDone(t, l, "op")
		if err := l.Kill("op"); err != nil {
			t.Fatalf("Kill(finished): %v", err)
		}
		st, ok := l.Status("op")
		if !ok {
			t.Fatal("op missing after kill")
		}
		if st.Killed || st.ExitCode != 0 || st.ErrMsg != "" {
			t.Errorf("status = %+v; want finished state untouched", st)
		}
	})
}

func TestLedger_GC_ReleasesExpiredEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		start := func(opID string) {
			t.Helper()
			if err := l.Start(opID, 60, echo(nil, 0)); err != nil {
				t.Fatalf("Start %s: %v", opID, err)
			}
			ledgerDone(t, l, opID)
		}
		// Done and past deadline + gcGrace when op-new starts: collected.
		start("op-expired")
		expired := l.entries["op-expired"].file.Name()
		time.Sleep(2*time.Hour - time.Minute)
		// Done but within retention: kept.
		start("op-recent")
		time.Sleep(time.Minute)
		start("op-new") // triggers gc
		if _, ok := l.Status("op-expired"); ok {
			t.Error("expired entry survived gc")
		}
		if _, err := os.Stat(expired); !os.IsNotExist(err) {
			t.Errorf("expired capture file not removed (stat err = %v)", err)
		}
		for _, opID := range []string{"op-recent", "op-new"} {
			if _, ok := l.Status(opID); !ok {
				t.Errorf("%s collected; want retained", opID)
			}
		}
	})
}

func TestLedger_Start_RejectsDuplicateOpID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLedger(t, t.TempDir())
		if err := l.Start("op", 60, echo([]byte("first"), 0)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := l.Start("op", 60, echo([]byte("second"), 0)); err == nil {
			t.Fatal("second Start with the same op id = nil; want a refusal")
		}
		ledgerDone(t, l, "op")
		if got, _, _ := collect(t, l.Frames(context.Background(), "op", 0)); string(got) != "first" {
			t.Errorf("output = %q; want the first op's, untouched by the duplicate", got)
		}
	})
}

func TestLedger_Discard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "guest")
		l := newLedger(t, dir)
		if err := l.Start("op", 3600, blockUntilDone(nil)); err != nil {
			t.Fatalf("Start: %v", err)
		}
		l.Discard()
		if _, ok := l.Status("op"); ok {
			t.Error("op survived Discard")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("ledger dir survived Discard (stat err = %v)", err)
		}
	})
}
