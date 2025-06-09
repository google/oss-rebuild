// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// MockCommandExecutor implements CommandExecutor for testing
type MockCommandExecutor struct {
	mu           sync.RWMutex
	commands     []MockCommand
	executeFunc  func(ctx context.Context, opts CommandOptions, name string, args ...string) error
	lookPathFunc func(file string) (string, error)
}

// MockCommand represents a command execution for verification
type MockCommand struct {
	Name  string
	Args  []string
	Input string
	Error error
}

// NewMockCommandExecutor creates a new mock command executor
func NewMockCommandExecutor() *MockCommandExecutor {
	return &MockCommandExecutor{
		commands: make([]MockCommand, 0),
	}
}

// SetExecuteFunc sets a custom function for Execute calls
func (m *MockCommandExecutor) SetExecuteFunc(f func(ctx context.Context, opts CommandOptions, name string, args ...string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeFunc = f
}

// SetLookPathFunc sets a custom function for LookPath calls
func (m *MockCommandExecutor) SetLookPathFunc(f func(file string) (string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookPathFunc = f
}

// Execute implements CommandExecutor with configurable options
func (m *MockCommandExecutor) Execute(ctx context.Context, opts CommandOptions, name string, args ...string) error {
	m.mu.Lock()
	if m.executeFunc != nil {
		f := m.executeFunc
		m.mu.Unlock()
		err := f(ctx, opts, name, args...)

		// Record command execution
		inputStr := ""
		if opts.Input != nil {
			if data, err := io.ReadAll(opts.Input); err == nil {
				inputStr = string(data)
			}
		}
		m.recordCommand(name, args, inputStr, err)
		return err
	}
	m.mu.Unlock()

	// Default behavior
	inputStr := ""
	if opts.Input != nil {
		if data, err := io.ReadAll(opts.Input); err == nil {
			inputStr = string(data)
		}
	}

	if opts.Output != nil {
		// Write mock output to the provided writer
		mockOutput := fmt.Sprintf("mock output for: %s %s\n", name, strings.Join(args, " "))
		opts.Output.Write([]byte(mockOutput))
	}

	m.recordCommand(name, args, inputStr, nil)
	return nil
}

// GetCommands returns all recorded commands for verification
func (m *MockCommandExecutor) GetCommands() []MockCommand {
	m.mu.RLock()
	defer m.mu.RUnlock()
	commands := make([]MockCommand, len(m.commands))
	copy(commands, m.commands)
	return commands
}

// Reset clears all recorded commands
func (m *MockCommandExecutor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = m.commands[:0]
}

// LookPath implements CommandExecutor
func (m *MockCommandExecutor) LookPath(file string) (string, error) {
	m.mu.RLock()
	f := m.lookPathFunc
	m.mu.RUnlock()
	if f != nil {
		return f(file)
	}
	// Default behavior - assume command exists
	return "/usr/bin/" + file, nil
}

// recordCommand records a command execution for later verification
func (m *MockCommandExecutor) recordCommand(name string, args []string, input string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, MockCommand{
		Name:  name,
		Args:  slices.Clone(args),
		Input: input,
		Error: err,
	})
}
