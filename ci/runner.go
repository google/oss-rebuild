// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var interactive = isTerminal()
var verbose bool
var continueOnFailure bool

// ANSI escape codes for terminal formatting
const (
	ansiReset       = "\033[0m"
	ansiRed         = "\033[0;31m"
	ansiGreen       = "\033[0;32m"
	ansiYellow      = "\033[0;33m"
	ansiDim         = "\033[0;90m"
	ansiClearLine   = "\033[2K"
	ansiCursorUpFmt = "\033[%dA"
)

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type task struct {
	name string
	fn   taskFn
}

type taskFn func(context.Context) (stdout, stderr string, err error)

type taskResult struct {
	name   string
	stdout string
	stderr string
	err    error
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var pendingDot = []string{"·", " "}

func isCancelled(err error) bool {
	return err == context.Canceled || strings.Contains(err.Error(), "signal: killed")
}

func runParallel(tasks []task) error {
	if interactive {
		return runParallelInteractive(tasks)
	}
	return runParallelSimple(tasks)
}

func runParallelSimple(tasks []task) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan taskResult, len(tasks))

	for _, t := range tasks {
		go func(t task) {
			stdout, stderr, err := t.fn(ctx)
			results <- taskResult{name: t.name, stdout: stdout, stderr: stderr, err: err}
		}(t)
	}

	var failed []taskResult
	for range tasks {
		r := <-results
		if r.err != nil {
			if isCancelled(r.err) {
				fmt.Printf("? %s\n", r.name)
			} else {
				fmt.Printf("✗ %s\n", r.name)
				failed = append(failed, r)
				if !continueOnFailure {
					cancel() // Cancel remaining tasks on first failure
				}
			}
		} else {
			fmt.Printf("✓ %s\n", r.name)
		}
	}

	if len(failed) > 0 {
		fmt.Println()
		for _, r := range failed {
			fmt.Printf("=== %s failed ===\n", r.name)
			printFailure(r, "")
		}
		return fmt.Errorf("%d task(s) failed", len(failed))
	}

	return nil
}

func runParallelInteractive(tasks []task) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan taskResult, len(tasks))
	status := make(map[string]string)
	var mu sync.Mutex

	for _, t := range tasks {
		status[t.name] = "running"
	}

	for _, t := range tasks {
		go func(t task) {
			stdout, stderr, err := t.fn(ctx)
			results <- taskResult{name: t.name, stdout: stdout, stderr: stderr, err: err}
		}(t)
	}

	for range tasks {
		fmt.Println()
	}

	done := make(chan []taskResult)
	go func() {
		var completed []taskResult
		for len(completed) < len(tasks) {
			select {
			case r := <-results:
				mu.Lock()
				if r.err != nil {
					if isCancelled(r.err) {
						status[r.name] = "cancelled"
					} else {
						status[r.name] = "fail"
						if !continueOnFailure {
							cancel() // Cancel remaining tasks on first failure
						}
					}
				} else {
					status[r.name] = "done"
				}
				mu.Unlock()
				completed = append(completed, r)
			default:
			}
			render(tasks, status, &mu)
			time.Sleep(80 * time.Millisecond)
		}
		done <- completed
	}()

	completed := <-done
	render(tasks, status, &mu)

	var failed []taskResult
	for _, r := range completed {
		if r.err != nil && !isCancelled(r.err) {
			failed = append(failed, r)
		}
	}

	if len(failed) > 0 {
		fmt.Println()
		for _, r := range failed {
			fmt.Printf(ansiRed+"✗ %s failed:"+ansiReset+"\n", r.name)
			printFailure(r, "  ")
		}
		return fmt.Errorf("%d task(s) failed", len(failed))
	}

	return nil
}

func runSequential(tasks []task) error {
	if interactive {
		return runSequentialInteractive(tasks)
	}
	return runSequentialSimple(tasks)
}

func runSequentialSimple(tasks []task) error {
	ctx := context.Background()
	for _, t := range tasks {
		stdout, stderr, err := t.fn(ctx)
		if err != nil {
			fmt.Printf("✗ %s\n", t.name)
			printFailure(taskResult{name: t.name, stdout: stdout, stderr: stderr, err: err}, "")
			if !continueOnFailure {
				return err
			}
		} else {
			fmt.Printf("✓ %s\n", t.name)
		}
	}
	return nil
}

func runSequentialInteractive(tasks []task) error {
	ctx := context.Background()
	status := make(map[string]string)
	for _, t := range tasks {
		status[t.name] = "pending"
	}

	// Print initial lines
	for range tasks {
		fmt.Println()
	}

	var mu sync.Mutex
	for _, t := range tasks {
		status[t.name] = "running"

		done := make(chan struct{})
		var stdout, stderr string
		var err error

		go func() {
			stdout, stderr, err = t.fn(ctx)
			close(done)
		}()

		// Animate while running
		for {
			select {
			case <-done:
				goto finished
			default:
				render(tasks, status, &mu)
				time.Sleep(80 * time.Millisecond)
			}
		}
	finished:

		if err != nil {
			status[t.name] = "fail"
			render(tasks, status, &mu)
			fmt.Printf("\n"+ansiRed+"✗ %s failed:"+ansiReset+"\n", t.name)
			printFailure(taskResult{name: t.name, stdout: stdout, stderr: stderr, err: err}, "  ")
			if !continueOnFailure {
				return err
			}
		} else {
			status[t.name] = "done"
		}
	}

	render(tasks, status, &mu)
	return nil
}

var frame int

func render(tasks []task, status map[string]string, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()

	fmt.Printf(ansiCursorUpFmt, len(tasks))

	frame++
	for _, t := range tasks {
		fmt.Print(ansiClearLine)
		switch status[t.name] {
		case "pending":
			dot := pendingDot[(frame/5)%len(pendingDot)]
			fmt.Printf(ansiDim+"%s"+ansiReset+" "+ansiDim+"%s"+ansiReset+"\n", dot, t.name)
		case "running":
			fmt.Printf(ansiYellow+"%s"+ansiReset+" %s\n", spinner[frame%len(spinner)], t.name)
		case "done":
			fmt.Printf(ansiGreen+"✓"+ansiReset+" %s\n", t.name)
		case "fail":
			fmt.Printf(ansiRed+"✗"+ansiReset+" %s\n", t.name)
		case "cancelled":
			fmt.Printf(ansiDim+"?"+ansiReset+" %s\n", t.name)
		}
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func printFailure(r taskResult, prefix string) {
	if r.stdout != "" {
		if prefix == "" {
			fmt.Println(r.stdout)
		} else {
			fmt.Println(indent(r.stdout, prefix))
		}
	}
	if r.err != nil && r.err.Error() != "exit status 1" {
		fmt.Printf("%s%v\n", prefix, r.err)
	}
}

func runQuiet(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf
	err := cmd.Run()
	return outbuf.String(), errbuf.String(), err
}

func runLoud(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &outbuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &errbuf)
	err := cmd.Run()
	return outbuf.String(), errbuf.String(), err
}

func runSingle(name string, fn taskFn) error {
	return runParallel([]task{{name: name, fn: fn}})
}
