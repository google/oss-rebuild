// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package execx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, h Handle) (error, time.Duration) {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case err := <-done:
		return err, time.Since(start)
	case <-time.After(10 * time.Second):
		t.Fatal("Wait never returned")
		return nil, 0
	}
}

func TestRealStart_StopGrace(t *testing.T) {
	exec := NewRealCommandExecutor()
	// A command that exits on TERM is reaped as soon as it does.
	ctx, cancel := context.WithCancel(context.Background())
	h, err := exec.Start(ctx, CommandOptions{StopGrace: 5 * time.Second}, "sleep", "300")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-h.Done():
		t.Fatal("Done closed while the command was running")
	default:
	}
	cancel()
	if _, took := waitFor(t, h); took > 2*time.Second {
		t.Errorf("a TERM-honoring command took %v to be reaped; want well under the grace", took)
	}
	select {
	case <-h.Done():
	default:
		t.Error("Done still open after Wait returned")
	}
	// Wait answers again with the same result.
	if err := h.Wait(); err == nil {
		t.Error("second Wait = nil; want the exit error of a signalled command")
	}
	// A command that ignores TERM is killed once the grace lapses.
	ctx, cancel = context.WithCancel(context.Background())
	h, err = exec.Start(ctx, CommandOptions{StopGrace: 200 * time.Millisecond}, "sh", "-c", "trap '' TERM; sleep 30")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the trap install
	cancel()
	if _, took := waitFor(t, h); took < 200*time.Millisecond || took > 5*time.Second {
		t.Errorf("a TERM-ignoring command was reaped after %v; want the grace, then a kill", took)
	}
}

func TestRealStart_ReportsAMissingBinary(t *testing.T) {
	if _, err := NewRealCommandExecutor().Start(context.Background(), CommandOptions{}, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Start of a missing binary = nil; want the exec error")
	}
}

func TestMockStart(t *testing.T) {
	mock := NewMockCommandExecutor()
	ctx, cancel := context.WithCancel(context.Background())
	h, err := mock.Start(ctx, CommandOptions{}, "guest", "--flag")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cmds := mock.GetCommands()
	if len(cmds) != 1 || cmds[0].Name != "guest" || cmds[0].Args[0] != "--flag" || cmds[0].Handle != h {
		t.Errorf("recorded %+v; want the started command with its handle", cmds)
	}
	// The command runs until its context is cancelled.
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case <-done:
		t.Fatal("Wait returned before cancel")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Wait after cancel = %v; want context.Canceled", err)
	}
	// A test can end a command on its own, the way a crash would.
	h2, _ := mock.Start(context.Background(), CommandOptions{}, "guest")
	crashed := errors.New("signal: killed")
	h2.(*MockHandle).Exit(crashed)
	if err := h2.Wait(); err != crashed {
		t.Errorf("Wait after Exit = %v; want the exit error", err)
	}
	// A custom function stands in for a command that cannot start.
	mock.SetStartFunc(func(context.Context, CommandOptions, string, ...string) (Handle, error) {
		return nil, errors.New("no such binary")
	})
	if _, err := mock.Start(context.Background(), CommandOptions{}, "guest"); err == nil {
		t.Error("Start with a failing function = nil; want its error")
	}
	if cmds := mock.GetCommands(); cmds[len(cmds)-1].Error == nil || cmds[len(cmds)-1].Handle != nil {
		t.Errorf("failed start recorded as %+v; want the error and no handle", cmds[len(cmds)-1])
	}
}

func TestRealStart_KillGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string // starts a child that ignores TERM and writes its pid to $1
		grace  time.Duration
	}{
		// The child inherits sh's output, so killing sh alone leaves it running
		// and holding Wait open.
		{"HoldsOutput", `trap '' TERM; sleep 30 & echo $! > "$1"; wait`, 0},
		{"HoldsOutputWithGrace", `trap '' TERM; sleep 30 & echo $! > "$1"; wait`, 100 * time.Millisecond},
		// sh exits on TERM, and the child, holding none of its output, would run
		// on past Wait.
		{"DetachedWithGrace", `(trap '' TERM; exec sleep 30) >/dev/null 2>&1 & echo $! > "$1"; wait`, 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithCancel(context.Background())
			var out strings.Builder
			h, err := NewRealCommandExecutor().Start(ctx, CommandOptions{Output: &out, StopGrace: tc.grace, KillGroup: true}, "sh", "-c", tc.script, "sh", pidFile)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			// Cancel once the child runs, however long sh takes to start it.
			var pid int
			deadline := time.Now().Add(10 * time.Second)
			for pid == 0 {
				if time.Now().After(deadline) {
					t.Fatal("the command never started its child")
				}
				time.Sleep(10 * time.Millisecond)
				b, _ := os.ReadFile(pidFile)
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			cancel()
			err, took := waitFor(t, h)
			if err == nil {
				t.Error("Wait() = nil, want the command killed")
			}
			if took > 3*time.Second {
				t.Errorf("Wait() returned after %v, want its child stopped with it", took)
			}
			deadline = time.Now().Add(2 * time.Second)
			for syscall.Kill(pid, 0) == nil {
				if time.Now().After(deadline) {
					syscall.Kill(pid, syscall.SIGKILL)
					t.Error("the command's child outlived it")
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestCommand_KillGroupCancelAfterExit(t *testing.T) {
	cmd, _ := command(context.Background(), CommandOptions{KillGroup: true}, "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A cancel that races the exit must not turn the success into an error.
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Cancel() after exit = %v, want os.ErrProcessDone", err)
	}
}

func TestRealExecute_EnvAndStderr(t *testing.T) {
	var out, errOut strings.Builder
	err := NewRealCommandExecutor().Execute(context.Background(), CommandOptions{Output: &out, Stderr: &errOut, Env: []string{"WORD=hi"}}, "sh", "-c", `echo "$WORD"; echo oops >&2`)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out.String() != "hi\n" || errOut.String() != "oops\n" {
		t.Errorf("stdout %q, stderr %q, want %q and %q", out.String(), errOut.String(), "hi\n", "oops\n")
	}
}
