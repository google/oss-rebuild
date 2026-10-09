// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package execx

import (
	"context"
	"errors"
	"path/filepath"
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
	if _, open := <-h.Done(), false; open {
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
