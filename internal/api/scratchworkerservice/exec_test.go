// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func newTestDeps(t *testing.T) (*ExecDeps, *ExecStore) {
	t.Helper()
	store := NewExecStore()
	return &ExecDeps{
		Store:   store,
		TempDir: t.TempDir(),
		Workdir: t.TempDir(),
	}, store
}

// stdReq builds a WorkerStartRequest with the minimum fields.
func stdReq(opID, envID string, cmd ...string) StartRequest {
	return StartRequest{OpID: opID, ScratchID: envID, Cmd: cmd}
}

func waitForDone(t *testing.T, store *ExecStore, opID string, timeout time.Duration) ExecStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, ok := store.status(opID)
		if ok && st.Done {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("op %s not done within %v", opID, timeout)
	return ExecStatus{}
}

func readAll(t *testing.T, store *ExecStore, opID string) []byte {
	t.Helper()
	b, err := store.readAll(opID)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	return b
}

func TestExecStart_NormalCompletion(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-normal"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "echo hi"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 3*time.Second)
	if st.ExitCode != 0 || st.TimedOut || st.ErrMsg != "" {
		t.Errorf("status = %+v; want clean exit", st)
	}
	if got := string(readAll(t, store, opID)); got != "hi\n" {
		t.Errorf("output = %q; want %q", got, "hi\n")
	}
}

func TestExecStart_NonZeroExit(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-nonzero"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "exit 7"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 3*time.Second)
	if st.ExitCode != 7 {
		t.Errorf("ExitCode = %d; want 7", st.ExitCode)
	}
}

func TestExecStart_Timeout(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-timeout"
	req := stdReq(opID, "env-1", "sleep", "30")
	req.TimeoutSeconds = 1
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 5*time.Second)
	if !st.TimedOut || st.ExitCode != 124 {
		t.Errorf("status = %+v; want TimedOut + 124", st)
	}
}

func TestExecStart_SpawnFail(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-spawnfail"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "/this/does/not/exist"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 3*time.Second)
	if st.ErrMsg == "" {
		t.Errorf("ErrMsg empty; want spawn failure")
	}
}

func TestExecStart_StdinEcho(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-stdin"
	payload := "piped via stdin"
	req := stdReq(opID, "env-1", "cat")
	req.StdinB64 = base64.StdEncoding.EncodeToString([]byte(payload))
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)
	if got := string(readAll(t, store, opID)); got != payload {
		t.Errorf("output = %q; want %q", got, payload)
	}
}

func TestExecStart_BadStdinB64(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-badstdin"
	req := stdReq(opID, "env-1", "cat")
	req.StdinB64 = "not-valid-base64!!!"
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 2*time.Second)
	if st.ErrMsg == "" || !strings.Contains(st.ErrMsg, "decode stdin") {
		t.Errorf("ErrMsg = %q; want decode-stdin failure", st.ErrMsg)
	}
}

func TestExecStart_NonBlocking(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-nonblock"
	req := stdReq(opID, "env-1", "sleep", "30")
	req.TimeoutSeconds = 1
	start := time.Now()
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("ExecStart took %v; want <100ms (must not block)", elapsed)
	}
	waitForDone(t, store, opID, 5*time.Second)
}

func TestExecStart_MergedStdoutStderr(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-merged"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "echo OUT; echo ERR 1>&2"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)
	got := string(readAll(t, store, opID))
	if !strings.Contains(got, "OUT") || !strings.Contains(got, "ERR") {
		t.Errorf("merged output = %q; want OUT and ERR both present", got)
	}
}

func TestExecStart_TimeoutPartialOutputPreserved(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-timeout-partial"
	req := stdReq(opID, "env-1", "sh", "-c", "echo before-timeout; sleep 30")
	req.TimeoutSeconds = 1
	if _, err := ExecStart(context.Background(), req, deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	st := waitForDone(t, store, opID, 5*time.Second)
	if !st.TimedOut {
		t.Fatalf("status = %+v; want TimedOut", st)
	}
	if got := string(readAll(t, store, opID)); !strings.Contains(got, "before-timeout") {
		t.Errorf("output = %q; want 'before-timeout' preserved", got)
	}
}

func TestExecStore_ReadAllReturnsFullBuffer(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-full-buf"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "printf abcdef"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)
	if got := string(readAll(t, store, opID)); got != "abcdef" {
		t.Errorf("readAll = %q; want %q", got, "abcdef")
	}
}

func TestExecStore_ForgetReleasesFile(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-forget"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "echo bye"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)
	store.Forget(opID)
	if _, ok := store.status(opID); ok {
		t.Errorf("status after Forget; want unknown")
	}
}

func TestStatus_UnknownOp(t *testing.T) {
	store := NewExecStore()
	deps := &StatusDeps{Store: store}
	if _, err := Status(context.Background(), StatusRequest{ID: "missing"}, deps); err == nil {
		t.Errorf("Status(missing) = nil; want error")
	}
}
