// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCommandExecutorOptions(t *testing.T) {
	executor := NewRealCommandExecutor()
	ctx := context.Background()

	t.Run("Basic mode with output discarded", func(t *testing.T) {
		err := executor.Execute(ctx, CommandOptions{}, "echo", "hello world")

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
	})

	t.Run("Streaming mode", func(t *testing.T) {
		var buf bytes.Buffer
		err := executor.Execute(ctx, CommandOptions{
			Output: &buf,
		}, "echo", "streaming test")

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		output := strings.TrimSpace(buf.String())
		if output != "streaming test" {
			t.Errorf("Expected 'streaming test', got '%s'", output)
		}
	})

	t.Run("Input mode", func(t *testing.T) {
		input := strings.NewReader("test input data")
		err := executor.Execute(ctx, CommandOptions{
			Input: input,
		}, "cat")

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
	})

	t.Run("Input and streaming combined", func(t *testing.T) {
		var buf bytes.Buffer
		input := strings.NewReader("combined test")
		err := executor.Execute(ctx, CommandOptions{
			Input:  input,
			Output: &buf,
		}, "cat")

		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		output := strings.TrimSpace(buf.String())
		if output != "combined test" {
			t.Errorf("Expected 'combined test', got '%s'", output)
		}
	})
}

func TestMockCommandExecutorOptions(t *testing.T) {
	mock := NewMockCommandExecutor()
	ctx := context.Background()

	t.Run("Custom execute function", func(t *testing.T) {
		// Set custom execute function
		mock.SetExecuteFunc(func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
			if name == "custom" && len(args) == 1 && args[0] == "test" {
				if opts.Output != nil {
					opts.Output.Write([]byte("custom output\n"))
				}
				return nil
			}
			return nil
		})

		// Test streaming mode
		var buf bytes.Buffer
		err := mock.Execute(ctx, CommandOptions{Output: &buf}, "custom", "test")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		output := strings.TrimSpace(buf.String())
		if output != "custom output" {
			t.Errorf("Expected 'custom output', got '%s'", output)
		}
	})

	t.Run("Default behavior", func(t *testing.T) {
		// Create a fresh mock without custom functions
		freshMock := NewMockCommandExecutor()

		var buf bytes.Buffer
		err := freshMock.Execute(ctx, CommandOptions{Output: &buf}, "default", "command")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		expectedOutput := "mock output for: default command"
		if !strings.Contains(buf.String(), expectedOutput) {
			t.Errorf("Expected output to contain '%s', got '%s'", expectedOutput, buf.String())
		}

		// Verify command was recorded
		commands := freshMock.GetCommands()
		if len(commands) != 1 {
			t.Fatalf("Expected exactly 1 command to be recorded, got %d", len(commands))
		}

		cmd := commands[0]
		if cmd.Name != "default" || len(cmd.Args) != 1 || cmd.Args[0] != "command" {
			t.Errorf("Command not recorded correctly: %+v", cmd)
		}
	})
}

func TestCommandExecutorLookPath(t *testing.T) {
	executor := NewRealCommandExecutor()

	// Test LookPath with a command that should exist
	path, err := executor.LookPath("echo")
	if err != nil {
		t.Fatalf("Expected to find echo command, got error: %v", err)
	}
	if path == "" {
		t.Error("Expected non-empty path for echo command")
	}

	// Test LookPath with a command that shouldn't exist
	_, err = executor.LookPath("nonexistent-command-12345")
	if err == nil {
		t.Error("Expected error for nonexistent command, but got none")
	}
}

func TestMockCommandExecutorLookPath(t *testing.T) {
	mock := NewMockCommandExecutor()

	// Test default behavior - should succeed
	path, err := mock.LookPath("docker")
	if err != nil {
		t.Fatalf("Expected default LookPath to succeed, got error: %v", err)
	}
	if path != "/usr/bin/docker" {
		t.Errorf("Expected '/usr/bin/docker', got '%s'", path)
	}

	// Test custom LookPath function
	mock.SetLookPathFunc(func(file string) (string, error) {
		if file == "custom-cmd" {
			return "/custom/path/custom-cmd", nil
		}
		return "", fmt.Errorf("command not found: %s", file)
	})

	path, err = mock.LookPath("custom-cmd")
	if err != nil {
		t.Fatalf("Expected custom LookPath to succeed, got error: %v", err)
	}
	if path != "/custom/path/custom-cmd" {
		t.Errorf("Expected '/custom/path/custom-cmd', got '%s'", path)
	}

	_, err = mock.LookPath("missing-cmd")
	if err == nil {
		t.Error("Expected custom LookPath to fail for missing command")
	}
}
