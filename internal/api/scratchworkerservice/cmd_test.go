// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Compress kill grace for tests so timeout cases finish quickly.
func init() { killGrace = 200 * time.Millisecond }

func TestRunCommand_ZeroExit(t *testing.T) {
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"sh", "-c", "exit 0"}}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if code != 0 {
		t.Errorf("code = %d; want 0", code)
	}
}

func TestRunCommand_NonZeroExit(t *testing.T) {
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"sh", "-c", "exit 7"}}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if code != 7 {
		t.Errorf("code = %d; want 7", code)
	}
}

func TestRunCommand_StdoutStderrCapture(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"sh", "-c", "echo out; echo err 1>&2"}}, &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("(code, err) = (%d, %v); want (0, nil)", code, err)
	}
	if got := out.String(); got != "out\n" {
		t.Errorf("stdout = %q; want %q", got, "out\n")
	}
	if got := errb.String(); got != "err\n" {
		t.Errorf("stderr = %q; want %q", got, "err\n")
	}
}

func TestRunCommand_StdinEcho(t *testing.T) {
	var out bytes.Buffer
	stdin := strings.NewReader("hello world")
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"cat"}, Stdin: stdin}, &out, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("(code, err) = (%d, %v); want (0, nil)", code, err)
	}
	if got := out.String(); got != "hello world" {
		t.Errorf("stdout = %q; want %q", got, "hello world")
	}
}

func TestRunCommand_Cwd(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"sh", "-c", "pwd"}, Cwd: dir}, &out, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("(code, err) = (%d, %v); want (0, nil)", code, err)
	}
	// macOS may resolve /var/folders/... via a symlink that adds /private; allow either.
	pwd := strings.TrimSpace(out.String())
	if pwd != dir && pwd != "/private"+dir {
		t.Errorf("pwd = %q; want %q (or /private prefix)", pwd, dir)
	}
}

func TestRunCommand_EnvOverride(t *testing.T) {
	var out bytes.Buffer
	code, err := runCommand(context.Background(),
		runSpec{
			Cmd: []string{"sh", "-c", "echo $FOO"},
			Env: map[string]string{"FOO": "bar"},
		}, &out, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("(code, err) = (%d, %v); want (0, nil)", code, err)
	}
	if got := strings.TrimSpace(out.String()); got != "bar" {
		t.Errorf("stdout = %q; want %q", got, "bar")
	}
}

func TestRunCommand_Timeout(t *testing.T) {
	start := time.Now()
	code, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"sleep", "30"}, TimeoutSeconds: 1}, io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v; want context.DeadlineExceeded", err)
	}
	if code != 124 {
		t.Errorf("code = %d; want 124", code)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("elapsed = %v; want < 3s", elapsed)
	}
}

func TestRunCommand_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := runCommand(ctx,
		runSpec{Cmd: []string{"sleep", "30"}}, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v; want NOT context.DeadlineExceeded (caller cancel)", err)
	}
}

func TestRunCommand_SpawnFail(t *testing.T) {
	_, err := runCommand(context.Background(),
		runSpec{Cmd: []string{"/this/does/not/exist"}}, io.Discard, io.Discard)
	if err == nil {
		t.Errorf("err = nil; want spawn failure")
	}
}

func TestRunCommand_EmptyCmd(t *testing.T) {
	_, err := runCommand(context.Background(),
		runSpec{Cmd: nil}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cmd is empty") {
		t.Errorf("err = %v; want 'cmd is empty'", err)
	}
}

func TestRunCommand_LargeStdoutToTempFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-output test in -short mode")
	}
	// ~110 MB via dd of zero bytes; stderr suppressed because macOS dd lacks
	// status=none.
	const want = int64(110 * 1024 * 1024)
	path := filepath.Join(t.TempDir(), "stdout.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer f.Close()

	code, err := runCommand(context.Background(), runSpec{
		Cmd: []string{"sh", "-c", "dd if=/dev/zero bs=1024 count=" + strconv.Itoa(int(want/1024)) + " 2>/dev/null"},
	}, f, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("(code, err) = (%d, %v); want (0, nil)", code, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() != want {
		t.Errorf("size = %d; want %d", st.Size(), want)
	}
}
