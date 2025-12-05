// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/spf13/cobra"
)

// Test types
type TestConfig struct {
	Name  string
	Value int
	Args  []string
}

func (c TestConfig) Validate() error {
	return nil
}

type TestDeps struct {
	IO IO
}

func (d *TestDeps) SetIO(cio IO) { d.IO = cio }

// Test action that writes to output
func testAction(ctx context.Context, cfg TestConfig, deps *TestDeps) (*act.NoOutput, error) {
	deps.IO.Out.Write([]byte("Hello " + cfg.Name))
	return &act.NoOutput{}, nil
}

func testInitDeps(ctx context.Context) (*TestDeps, error) {
	return &TestDeps{}, nil
}

func TestSkipArgs(t *testing.T) {
	cfg := &TestConfig{}
	err := SkipArgs(cfg, []string{})
	if err != nil {
		t.Errorf("NoArgs() error = %v, wantErr %v", err, nil)
	}
}

func TestRunE(t *testing.T) {
	cfg := TestConfig{Name: "World"}

	// Create a cobra command with our RunE
	cmd := &cobra.Command{
		Use: "test",
		RunE: RunE(
			&cfg,
			SkipArgs[TestConfig],
			testInitDeps,
			testAction,
		),
	}

	// Capture output
	var outBuf bytes.Buffer
	cmd.SetOut(&outBuf)

	// Execute
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Check output
	got := outBuf.String()
	want := "Hello World"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
