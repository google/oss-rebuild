// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"fmt"
	"os"
	"slices"
)

func main() {
	args := os.Args[1:]
	if slices.Contains(args, "-v") {
		verbose = true
		interactive = false
		args = slices.DeleteFunc(args, func(s string) bool { return s == "-v" })
	}
	if slices.Contains(args, "-keep-going") {
		continueOnFailure = true
		args = slices.DeleteFunc(args, func(s string) bool { return s == "-keep-going" })
	}

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	// Resolve args to a flat task list.
	var allTasks []task
	var sequential bool
	for _, name := range args {
		if fn, ok := tasks[name]; ok {
			allTasks = append(allTasks, task{name: name, fn: fn})
		} else if comp, ok := composites[name]; ok {
			allTasks = append(allTasks, comp.tasks...)
			sequential = cmp.Or(sequential, comp.sequential)
		} else {
			fmt.Fprintf(os.Stderr, "unknown command: %s\n", name)
			usage()
			os.Exit(1)
		}
	}

	var err error
	if sequential {
		err = runSequential(allTasks)
	} else {
		err = runParallel(allTasks)
	}
	if err != nil {
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Usage: go run ./ci [-v] [-keep-going] <command> [command...]

Multiple commands are run in parallel. Composite commands (check, fix)
expand to their own parallel/sequential task sets.

Options:
  -v             Verbose output (stream command output to terminal)
  -keep-going    Run all tasks to completion, don't stop on first failure

Commands:
  build          Build all packages
  test           Run tests
  lint           Run go vet
  fmt            Format code
  fmt-check      Check formatting
  imports        Format imports
  imports-check  Check import formatting
  license        Add license headers
  license-check  Check license headers
  check          Run all checks (CI)
  fix            Run all fixers
`)
}
