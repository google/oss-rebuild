// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
)

// --- Task builders ---

func run(args ...string) taskFn {
	return func(ctx context.Context) (string, string, error) {
		if verbose {
			return runLoud(ctx, args[0], args[1:]...)
		}
		return runQuiet(ctx, args[0], args[1:]...)
	}
}

func (fn taskFn) failIfOutput(msg string) taskFn {
	return func(ctx context.Context) (string, string, error) {
		stdout, stderr, err := fn(ctx)
		if err != nil {
			return stdout, stderr, err
		}
		if strings.TrimSpace(stdout) != "" {
			return msg + ":\n" + stdout, stderr, errors.New(msg)
		}
		return "", stderr, nil
	}
}

// --- Task definitions ---

var (
	buildTask = run("go", "build", "./...")
	testTask  = run("go", "test", "./...")
	lintTask  = run("go", "vet", "./...")

	fmtCheckTask = run("sh", "-c", "find . -name '*.go' ! -name '*.pb.go' -print0 | xargs -0 gofmt -l").failIfOutput("files need formatting")
	fmtFixTask   = run("sh", "-c", "find . -name '*.go' ! -name '*.pb.go' -print0 | xargs -0 gofmt -w")

	importsCheckTask = run("sh", "-c", "find . -name '*.go' ! -name '*.pb.go' -print0 | xargs -0 go tool goimports -l").failIfOutput("files need import formatting")
	importsFixTask   = run("sh", "-c", "find . -name '*.go' ! -name '*.pb.go' -print0 | xargs -0 go tool goimports -w")

	// NOTE: docs/layouts are Hugo Go templates which do not carry the correct file extension.
	licenseCheckTask = run("go", "tool", "addlicense", "-check", "-s=only", "-ignore=.*/**", "-ignore=bin/**", "-ignore=**/.terraform.lock.hcl", "-ignore=definitions/**", "-ignore=docs/layouts/**", "-ignore=**/*.pb.go", ".")
	licenseFixTask   = run("go", "tool", "addlicense" /*     */, "-s=only", "-ignore=.*/**", "-ignore=bin/**", "-ignore=**/.terraform.lock.hcl", "-ignore=definitions/**", "-ignore=docs/layouts/**", "-ignore=**/*.pb.go", ".")
)

// --- Commands ---

var tasks = map[string]taskFn{
	"build":         buildTask,
	"test":          testTask,
	"lint":          lintTask,
	"fmt-check":     fmtCheckTask,
	"imports-check": importsCheckTask,
	"license-check": licenseCheckTask,
}

// --- Composite commands ---

// Mutating commands are composites to ensure sequential execution when
// combined with other mutating commands (e.g. "fmt imports" must not race).
var composites = map[string]composite{
	"fmt":     {tasks: []task{{"fmt", fmtFixTask}}, sequential: true},
	"imports": {tasks: []task{{"imports", importsFixTask}}, sequential: true},
	"license": {tasks: []task{{"license", licenseFixTask}}, sequential: true},
	"check": {tasks: []task{
		{"build", buildTask},
		{"test", testTask},
		{"lint", lintTask},
		{"fmt", fmtCheckTask},
		{"imports", importsCheckTask},
		{"license", licenseCheckTask},
	}},
	"fix": {tasks: []task{
		{"fmt", fmtFixTask},
		{"imports", importsFixTask},
		{"license", licenseFixTask},
	}, sequential: true},
}
