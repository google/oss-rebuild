// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package execx runs external commands behind an interface a test can
// stand in for.
package execx

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// CommandOptions configures command execution
type CommandOptions struct {
	// Input provides stdin to the command
	Input io.Reader
	// Output streams stdout/stderr to the writer (if nil, output is discarded)
	Output io.Writer
	// Dir is the directory in which the command is run
	Dir string
	// StopGrace makes cancellation signal termination and wait before killing.
	StopGrace time.Duration
}

// CommandExecutor abstracts command execution for better testability
type CommandExecutor interface {
	// Execute runs a command with the given options, returns error on failure
	// Comparable to exec.CommandContext(...).Run()
	Execute(ctx context.Context, opts CommandOptions, name string, args ...string) error
	// Start launches a command as Exec but returns with a Handle instead of blocking.
	// Cancelling ctx stops the command. Comparable to exec.CommandContext(...).Start()
	Start(ctx context.Context, opts CommandOptions, name string, args ...string) (Handle, error)
	// LookPath searches for an executable named file in the directories named by the PATH environment variable
	// Comparable to exec.LookPath()
	LookPath(file string) (string, error)
}

// Handle is a started command. Done is closed once the command has exited, and
// Wait blocks until then, returning the exit error (see exec.Cmd.Wait).
// Both may be used repeatedly.
type Handle interface {
	Done() <-chan struct{}
	Wait() error
}

// realCommandExecutor implements CommandExecutor using os/exec
type realCommandExecutor struct{}

// NewRealCommandExecutor creates a new CommandExecutor that uses os/exec
func NewRealCommandExecutor() CommandExecutor {
	return &realCommandExecutor{}
}

// command builds the exec.Cmd for opts.
func command(ctx context.Context, opts CommandOptions, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Input != nil {
		cmd.Stdin = opts.Input
	}
	if opts.Output != nil {
		cmd.Stdout = opts.Output
		cmd.Stderr = opts.Output
	}
	cmd.Dir = opts.Dir
	if opts.StopGrace > 0 {
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = opts.StopGrace
	}
	return cmd
}

// Execute implements CommandExecutor with configurable options
func (r *realCommandExecutor) Execute(ctx context.Context, opts CommandOptions, name string, args ...string) error {
	// Block and wait for completion.
	return command(ctx, opts, name, args...).Run()
}

// started is the real Handle. A goroutine collects the exit once, so Done and
// Wait answer any number of times.
type started struct {
	done chan struct{}
	err  error
}

func (s *started) Done() <-chan struct{} { return s.done }

func (s *started) Wait() error {
	<-s.done
	return s.err
}

// Start implements CommandExecutor.
func (r *realCommandExecutor) Start(ctx context.Context, opts CommandOptions, name string, args ...string) (Handle, error) {
	cmd := command(ctx, opts, name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &started{done: make(chan struct{})}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

// LookPath implements CommandExecutor
func (r *realCommandExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}
