// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"io"
	"os/exec"
)

// CommandOptions configures command execution
type CommandOptions struct {
	// Input provides stdin to the command
	Input io.Reader
	// Output streams stdout/stderr to the writer (if nil, output is discarded)
	Output io.Writer
	// Dir is the directory in which the command is run
	Dir string
}

// CommandExecutor abstracts command execution for better testability
type CommandExecutor interface {
	// Execute runs a command with the given options, returns error on failure
	// Comparable to exec.CommandContext(...).Run()
	Execute(ctx context.Context, opts CommandOptions, name string, args ...string) error
	// LookPath searches for an executable named file in the directories named by the PATH environment variable
	// Comparable to exec.LookPath()
	LookPath(file string) (string, error)
}

// realCommandExecutor implements CommandExecutor using os/exec
type realCommandExecutor struct{}

// NewRealCommandExecutor creates a new CommandExecutor that uses os/exec
func NewRealCommandExecutor() CommandExecutor {
	return &realCommandExecutor{}
}

// Execute implements CommandExecutor with configurable options
func (r *realCommandExecutor) Execute(ctx context.Context, opts CommandOptions, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Input != nil {
		cmd.Stdin = opts.Input
	}
	if opts.Output != nil {
		cmd.Stdout = opts.Output
		cmd.Stderr = opts.Output
	}
	cmd.Dir = opts.Dir
	// Block and wait for completion.
	return cmd.Run()
}

// LookPath implements CommandExecutor
func (r *realCommandExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}
