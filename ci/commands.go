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

	fmtCheckTask = run("gofmt", "-l", ".").failIfOutput("files need formatting")
	fmtFixTask   = run("gofmt", "-w", ".")

	importsCheckTask = run("go", "tool", "goimports", "-l", ".").failIfOutput("files need import formatting")
	importsFixTask   = run("go", "tool", "goimports", "-w", ".")

	// NOTE: docs/layouts are Hugo Go templates which do not carry the correct file extension.
	licenseCheckTask = run("go", "tool", "addlicense", "-check", "-s=only", "-ignore=.*/**", "-ignore=bin/**", "-ignore=**/.terraform.lock.hcl", "-ignore=definitions/**", "-ignore=docs/layouts/**", ".")
	licenseFixTask   = run("go", "tool", "addlicense" /*     */, "-s=only", "-ignore=.*/**", "-ignore=bin/**", "-ignore=**/.terraform.lock.hcl", "-ignore=definitions/**", "-ignore=docs/layouts/**", ".")
)

// --- Single commands ---

func build() error        { return runSingle("build", buildTask) }
func test() error         { return runSingle("test", testTask) }
func lint() error         { return runSingle("lint", lintTask) }
func fmtCheck() error     { return runSingle("fmt-check", fmtCheckTask) }
func importsCheck() error { return runSingle("imports-check", importsCheckTask) }
func licenseCheck() error { return runSingle("license-check", licenseCheckTask) }

func fmtFix() error     { return runSingle("fmt", fmtFixTask) }
func importsFix() error { return runSingle("imports", importsFixTask) }
func licenseFix() error { return runSingle("license", licenseFixTask) }

// --- Composite commands ---

func check() error {
	return runParallel([]task{
		{"build", buildTask},
		{"test", testTask},
		{"lint", lintTask},
		{"fmt", fmtCheckTask},
		{"imports", importsCheckTask},
		{"license", licenseCheckTask},
	})
}

func fix() error {
	return runSequential([]task{
		{"fmt", fmtFixTask},
		{"imports", importsFixTask},
		{"license", licenseFixTask},
	})
}
