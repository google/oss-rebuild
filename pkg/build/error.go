// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bytes"
	"fmt"
	"strings"
)

// ExitError is returned via Result.Error when a build phase exits nonzero.
// Callers distinguish build failures from infrastructure failures with
// errors.As.
type ExitError struct {
	// Code is the exit status, when known. Zero means the executor could
	// not observe it (Cloud Build's run sentinel replaces the container's).
	Code int
	// Phase is the build phase that exited: a DockerRunPlan phase name
	// ("setup", "source", "deps", "build") or a docker build stage
	// ("image build", "container run").
	Phase string
	// Command is the last command the phase's set -x trace echoed, set
	// best-effort by the executor. Empty when unknown.
	Command string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("build failed in %s phase", e.Phase)
	if e.Code != 0 {
		msg += fmt.Sprintf(" with exit code %d", e.Code)
	}
	if e.Command != "" {
		msg += fmt.Sprintf("; the failing command was `%s`", e.Command)
	}
	return msg
}

// TracedCommand returns the command a set -x trace line echoes (PS4 "+",
// repeated per subshell depth in bash), or "" for any other line.
func TracedCommand(line string) string {
	trimmed := strings.TrimLeft(line, "+")
	if trimmed == line {
		return ""
	}
	return strings.TrimSpace(trimmed)
}

// LastTracedCommand returns the command of out's last set -x trace line, or
// "" if none.
func LastTracedCommand(out []byte) string {
	var last string
	for line := range bytes.Lines(out) {
		if cmd := TracedCommand(string(line)); cmd != "" {
			last = cmd
		}
	}
	return last
}
