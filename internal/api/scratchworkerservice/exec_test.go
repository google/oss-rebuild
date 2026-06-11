// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
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

// collectOutput drains the output iterator into the concatenated content,
// the per-frame offsets observed, and the terminal error (nil on clean
// end).
func collectOutput(t *testing.T, store *ExecStore, opID string, offset int64) (content []byte, offsets []int64, err error) {
	t.Helper()
	for frame, e := range store.outputFrom(context.Background(), opID, offset) {
		if e != nil {
			err = e
			return
		}
		offsets = append(offsets, frame.Offset)
		content = append(content, frame.Content...)
	}
	return
}

func readAll(t *testing.T, store *ExecStore, opID string) []byte {
	t.Helper()
	content, _, err := collectOutput(t, store, opID, 0)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	return content
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
	// Registration is synchronous: a status poll arriving any time after
	// /exec/start returns must find the op.
	if _, ok := store.status(opID); !ok {
		t.Error("op not in store at ExecStart return; want synchronous registration")
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

func TestExecStore_GCReleasesExpiredEntries(t *testing.T) {
	store := NewExecStore()
	now := time.Now().UTC()
	tmpFile := func() *os.File {
		f, err := os.CreateTemp(t.TempDir(), "exec-out-*")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		return f
	}
	expired := tmpFile()
	// Done and past startedAt + timeout + gcGrace: collected.
	store.create("op-expired", expired, now.Add(-2*time.Hour), 60)
	store.finish("op-expired", 0, nil, 0)
	// Done but within retention: kept.
	store.create("op-recent", tmpFile(), now.Add(-time.Minute), 60)
	store.finish("op-recent", 0, nil, 0)
	// Done, ancient, but unbounded: kept for the VM lifetime.
	store.create("op-unbounded", tmpFile(), now.Add(-24*time.Hour), 0)
	store.finish("op-unbounded", 0, nil, 0)

	store.create("op-new", tmpFile(), now, 60) // triggers gc

	if _, ok := store.status("op-expired"); ok {
		t.Errorf("expired entry survived gc")
	}
	if _, err := os.Stat(expired.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expired entry's temp file not removed (stat err=%v)", err)
	}
	for _, opID := range []string{"op-recent", "op-unbounded", "op-new"} {
		if _, ok := store.status(opID); !ok {
			t.Errorf("%s collected; want retained", opID)
		}
	}
}

func TestStatus_UnknownOp(t *testing.T) {
	store := NewExecStore()
	deps := &StatusDeps{Store: store}
	if _, err := Status(context.Background(), StatusRequest{ID: "missing"}, deps); err == nil {
		t.Errorf("Status(missing) = nil; want error")
	}
}

func TestOutput_FromZero(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-stream-zero"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "printf 'hello stream'"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)

	got, offsets, err := collectOutput(t, store, opID, 0)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if string(got) != "hello stream" {
		t.Errorf("content = %q, want %q", got, "hello stream")
	}
	if len(offsets) == 0 || offsets[0] != 0 {
		t.Errorf("offsets = %v, want first 0", offsets)
	}
}

func TestOutput_FromMidOffset(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-stream-mid"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "printf 0123456789"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)

	got, offsets, err := collectOutput(t, store, opID, 4)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if string(got) != "456789" {
		t.Errorf("content = %q, want %q", got, "456789")
	}
	if offsets[0] != 4 {
		t.Errorf("first offset = %d, want 4", offsets[0])
	}
}

func TestOutput_OffsetAtOrPastEnd(t *testing.T) {
	deps, store := newTestDeps(t)
	const opID = "op-stream-past"
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", "sh", "-c", "printf abc"), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 3*time.Second)

	got, offsets, err := collectOutput(t, store, opID, 100)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("content = %q, want empty", got)
	}
	if len(offsets) != 0 {
		t.Errorf("offsets = %v, want none", offsets)
	}
}

func TestOutput_MultiChunk(t *testing.T) {
	// Produce enough output to span multiple stream chunks.
	deps, store := newTestDeps(t)
	const opID = "op-stream-multi"
	// outputChunkSize * 2.5 bytes of "x" via printf %0Nd (faster than yes/dd in tests).
	const total = outputChunkSize * 5 / 2
	cmd := []string{"sh", "-c", "head -c " + strconv.Itoa(total) + " /dev/zero | tr '\\0' x"}
	if _, err := ExecStart(context.Background(), stdReq(opID, "env-1", cmd...), deps); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	waitForDone(t, store, opID, 5*time.Second)

	got, offsets, err := collectOutput(t, store, opID, 0)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != total {
		t.Errorf("len(content) = %d, want %d", len(got), total)
	}
	if len(offsets) < 3 {
		t.Errorf("got %d frames, want >=3 (chunk size = %d)", len(offsets), outputChunkSize)
	}
	// Offsets are contiguous and monotonic.
	var pos int64
	for i, o := range offsets {
		if o != pos {
			t.Errorf("offsets[%d] = %d, want %d", i, o, pos)
			break
		}
		// All but the last frame should be exactly chunkSize.
		if i < len(offsets)-1 {
			pos += outputChunkSize
		}
	}
}

func TestOutput_UnknownOp(t *testing.T) {
	store := NewExecStore()
	_, _, err := collectOutput(t, store, "missing", 0)
	if err == nil {
		t.Errorf("stream(missing) = nil; want error")
	}
}
