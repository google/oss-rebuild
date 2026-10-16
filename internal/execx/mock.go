// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package execx

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
	startFunc    func(ctx context.Context, opts CommandOptions, name string, args ...string) (Handle, error)
	lookPathFunc func(file string) (string, error)
}

// MockCommand represents a command execution for verification
type MockCommand struct {
	Name  string
	Args  []string
	Input string
	Error error
	// Handle is what Start returned, nil for an Execute or a Start that
	// failed.
	Handle *MockHandle
}

// MockHandle is the Handle the mock's Start returns. The command ends when
// its context is cancelled, or when a test ends it with Exit.
type MockHandle struct {
	once sync.Once
	done chan struct{}
	err  error
}

// Done implements Handle.
func (h *MockHandle) Done() <-chan struct{} { return h.done }

// Wait implements Handle.
func (h *MockHandle) Wait() error {
	<-h.done
	return h.err
}

// Exit ends the command with err, standing in for a command that exited
// on its own.
func (h *MockHandle) Exit(err error) {
	h.once.Do(func() {
		h.err = err
		close(h.done)
	})
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

// SetStartFunc sets a custom function for Start calls
func (m *MockCommandExecutor) SetStartFunc(f func(ctx context.Context, opts CommandOptions, name string, args ...string) (Handle, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startFunc = f
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
	f := m.executeFunc
	m.mu.Unlock()
	if f != nil {
		err := f(ctx, opts, name, args...)
		m.record(MockCommand{Name: name, Args: slices.Clone(args), Input: readInput(opts), Error: err})
		return err
	}
	// Default behavior
	input := readInput(opts)
	if opts.Output != nil {
		// Write mock output to the provided writer
		mockOutput := fmt.Sprintf("mock output for: %s %s\n", name, strings.Join(args, " "))
		opts.Output.Write([]byte(mockOutput))
	}
	m.record(MockCommand{Name: name, Args: slices.Clone(args), Input: input})
	return nil
}

// Start implements CommandExecutor. Without a custom function the command
// is recorded and runs until its context is cancelled.
func (m *MockCommandExecutor) Start(ctx context.Context, opts CommandOptions, name string, args ...string) (Handle, error) {
	m.mu.Lock()
	f := m.startFunc
	m.mu.Unlock()
	if f != nil {
		h, err := f(ctx, opts, name, args...)
		mh, _ := h.(*MockHandle)
		m.record(MockCommand{Name: name, Args: slices.Clone(args), Input: readInput(opts), Error: err, Handle: mh})
		return h, err
	}
	h := &MockHandle{done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		h.Exit(ctx.Err())
	}()
	m.record(MockCommand{Name: name, Args: slices.Clone(args), Input: readInput(opts), Handle: h})
	return h, nil
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

// record appends a command execution for later verification
func (m *MockCommandExecutor) record(c MockCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, c)
}

// readInput drains opts.Input for the record, empty when there is none.
func readInput(opts CommandOptions) string {
	if opts.Input == nil {
		return ""
	}
	data, err := io.ReadAll(opts.Input)
	if err != nil {
		return ""
	}
	return string(data)
}
