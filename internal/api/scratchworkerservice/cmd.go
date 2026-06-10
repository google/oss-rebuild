// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scratchworkerservice implements the per-VM scratch worker.
package scratchworkerservice

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is the time the runner waits after SIGTERM before SIGKILLing the
// process group. Tunable for tests.
var killGrace = 5 * time.Second

// runSpec holds the inputs runCommand needs. Kept package-private as the
// internal exec runner shape, independent of the wire-level StartRequest.
type runSpec struct {
	Cmd            []string
	Cwd            string
	Env            map[string]string
	Stdin          io.Reader
	TimeoutSeconds int
}

// runCommand spawns Cmd[0] with Cmd[1:] arguments, writing stdout/stderr to
// the supplied writers. Stdin (if non-nil) is piped to the process. The
// process is started in its own process group so timeout/cancel can reach
// the entire subtree.
//
// Return semantics:
//   - normal exit (any code): (code, nil)
//   - command exceeded TimeoutSeconds: (124, context.DeadlineExceeded)
//   - context cancelled by caller (not deadline): (0, context.Canceled)
//   - spawn or unexpected wait failure: (0, err)
//
// Callers detect the timeout case with errors.Is(err, context.DeadlineExceeded).
func runCommand(ctx context.Context, spec runSpec, stdout, stderr io.Writer) (exitCode int, err error) {
	if len(spec.Cmd) == 0 {
		return 0, errors.New("cmd is empty")
	}

	runCtx := ctx
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	cmd := exec.Command(spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Stdin = spec.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Dir = spec.Cwd
	cmd.Env = buildEnv(spec.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pgid := cmd.Process.Pid // Setpgid makes the child its own pgrp leader.

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		return classifyExit(cmd, waitErr)
	case <-runCtx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(killGrace):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return 124, context.DeadlineExceeded
		}
		return 0, runCtx.Err()
	}
}

func classifyExit(cmd *exec.Cmd, err error) (int, error) {
	if err == nil {
		return cmd.ProcessState.ExitCode(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// buildEnv returns the env slice for exec.Cmd. nil means "inherit parent's
// environment" per exec docs. A non-empty extra map is layered on top of
// os.Environ(); since exec uses the last value for duplicate keys, the
// extras override the inherited values.
func buildEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
