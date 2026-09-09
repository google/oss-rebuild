// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestPosixSingleQuote round-trips tricky commands through a real /bin/sh: the
// quoted word must reproduce the input verbatim, with no expansion of $(...),
// backticks, or embedded quotes. run_in_container relies on this to pass the
// LLM's command into `docker exec ... /bin/sh -c <quoted>` without the outer
// shell interpreting it.
func TestPosixSingleQuote(t *testing.T) {
	for _, in := range []string{
		`echo hi`,
		`echo 'single quoted'`,
		`python3 -c "import sys; print('ok')"`,
		`a'b'c`,
		`$(touch pwned)`,
		"`whoami`",
		`back\slash and "double"`,
		"tab\tand space",
	} {
		script := "printf %s " + posixSingleQuote(in)
		out, err := exec.Command("/bin/sh", "-c", script).Output()
		if err != nil {
			t.Fatalf("sh -c %q: %v", script, err)
		}
		if string(out) != in {
			t.Errorf("round-trip mismatch: got %q want %q", out, in)
		}
	}
}

// TestDockerExecInContainerScript pins the wrapper's structure: resolve the
// single retained rb-* container, guard against no build having run, start it
// idempotently, and exec the (safely quoted) command inside it.
func TestDockerExecInContainerScript(t *testing.T) {
	s := dockerExecInContainerScript(`echo 'hi'`)
	for _, want := range []string{
		`docker ps -aq --filter name=^rb-`,
		`exit 125`,
		`docker start "$c"`,
		`exec docker exec "$c" /bin/sh -c '`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// TestShellTimeout covers the arg coercion: JSON numbers arrive as float64,
// zero means "use the default", and values past the cap clamp to it.
func TestShellTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    int
		wantErr bool
	}{
		{name: "Absent", args: map[string]any{}, want: 0},
		{name: "JSONNumber", args: map[string]any{"timeout_seconds": float64(90)}, want: 90},
		{name: "Int", args: map[string]any{"timeout_seconds": 45}, want: 45},
		{name: "ClampedToMax", args: map[string]any{"timeout_seconds": float64(runCommandTimeoutMax + 1)}, want: runCommandTimeoutMax},
		{name: "NonNumeric", args: map[string]any{"timeout_seconds": "60"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errStr := shellTimeout(tc.args)
			if (errStr != "") != tc.wantErr {
				t.Fatalf("shellTimeout() error = %q, wantErr %v", errStr, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("shellTimeout() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDetachOutput runs the wrapper through a real /bin/sh: the call returns
// when the command's own shell exits even though the child is backgrounded and
// still holds the output, and the output and exit code intact.
// NOTE: The child outlives the call, so the test remains fast.
func TestDetachOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", detachOutput("echo out; echo err >&2; sleep 1 & exit 3")).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit code 3", err)
	}
	if got := string(out); got != "out\nerr\n" {
		t.Errorf("output = %q, want the command's stdout and stderr in order", got)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("call took %v, want a return when the shell exits, not when its child does", took)
	}
}
