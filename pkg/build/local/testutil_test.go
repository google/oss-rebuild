// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMockCommandExecutor(t *testing.T) {
	mock := NewMockCommandExecutor()

	// Test Execute method
	var buf bytes.Buffer
	err := mock.Execute(context.Background(), CommandOptions{Output: &buf}, "echo", "hello")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "mock output for: echo hello") {
		t.Errorf("Unexpected output: %s", buf.String())
	}

	// Test Execute method with input
	buf.Reset()
	input := strings.NewReader("test input")
	err = mock.Execute(context.Background(), CommandOptions{Input: input, Output: &buf}, "cat")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "mock output for: cat") {
		t.Errorf("Unexpected output: %s", buf.String())
	}

	// Verify commands were recorded
	commands := mock.GetCommands()
	if len(commands) != 2 {
		t.Errorf("Expected 2 commands, got %d", len(commands))
	}

	if commands[0].Name != "echo" || len(commands[0].Args) != 1 || commands[0].Args[0] != "hello" {
		t.Errorf("First command not recorded correctly: %+v", commands[0])
	}

	if commands[1].Name != "cat" || commands[1].Input != "test input" {
		t.Errorf("Second command not recorded correctly: %+v", commands[1])
	}
}
