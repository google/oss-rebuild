// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
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

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	commands := map[string]func() error{
		"build":         build,
		"test":          test,
		"lint":          lint,
		"fmt":           fmtFix,
		"fmt-check":     fmtCheck,
		"imports":       importsFix,
		"imports-check": importsCheck,
		"license":       licenseFix,
		"license-check": licenseCheck,
		"check":         check,
		"fix":           fix,
	}

	cmd, ok := commands[args[0]]
	if !ok {
		usage()
		os.Exit(1)
	}

	if err := cmd(); err != nil {
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Usage: go run ./ci [-v] <command>

Options:
  -v             Verbose output (stream command output to terminal)

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
