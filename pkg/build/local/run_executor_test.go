// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// Mock types for testing
type mockPlanner struct {
	plan *DockerRunPlan
	err  error
}

func (m *mockPlanner) GeneratePlan(ctx context.Context, input rebuild.Input, opts build.PlanOptions) (*DockerRunPlan, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.plan, nil
}

func newMockAssetStore() rebuild.LocatableAssetStore {
	return rebuild.NewFilesystemAssetStore(memfs.New())
}

// Test case structure
type testCase struct {
	name             string
	plan             *DockerRunPlan
	planError        error
	input            rebuild.Input
	options          build.Options
	maxParallel      int
	dockerCmd        string
	executeFunc      func(ctx context.Context, opts CommandOptions, name string, args ...string) error
	lookPathFunc     func(file string) (string, error)
	expectedCommands []MockCommand
	expectedError    string
	expectSuccess    bool
}

func TestDockerRunExecutor(t *testing.T) {
	testCases := []testCase{
		{
			name: "successful execution",
			plan: &DockerRunPlan{
				Image:      "alpine:3.19",
				Command:    []string{"echo", "hello"},
				OutputPath: "/out/result.txt",
				WorkingDir: "/workspace",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-pkg",
					Version:   "1.0.0",
					Artifact:  "test-pkg-1.0.0.tgz",
				},
			},
			options: build.Options{
				BuildID: "test-build-123",
				Resources: build.Resources{
					AssetStore: newMockAssetStore(),
				},
			},
			maxParallel: 2,
			dockerCmd:   "docker",
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if opts.Output != nil {
					opts.Output.Write([]byte("Build successful\n"))
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name: "docker",
					Args: []string{"run", "--rm", "--name", "test-build-123", "-v", "/tmp/oss-rebuild-test-build-123:/out", "-w", "/workspace", "alpine:3.19", "echo", "hello"},
				},
			},
			expectSuccess: true,
		},
		{
			name: "docker command not found",
			plan: &DockerRunPlan{
				Image:      "alpine:3.19",
				Command:    []string{"echo", "hello"},
				OutputPath: "/out/result.txt",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.PyPI,
					Package:   "test-pkg",
					Version:   "1.0.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-456",
			},
			maxParallel: 1,
			lookPathFunc: func(file string) (string, error) {
				return "", errors.New("docker: command not found")
			},
			expectedError: "docker command not found",
		},
		{
			name: "docker execution failure",
			plan: &DockerRunPlan{
				Image:      "alpine:3.19",
				Command:    []string{"false"},
				OutputPath: "/out/result.txt",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.CratesIO,
					Package:   "test-crate",
					Version:   "0.1.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-789",
			},
			maxParallel: 1,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				return errors.New("exit status 1")
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"run", "--rm", "--name", "test-build-789", "-v", "/tmp/oss-rebuild-test-build-789:/out", "alpine:3.19", "false"},
					Error: errors.New("exit status 1"),
				},
			},
			expectSuccess: false,
		},
		{
			name:      "plan generation failure",
			planError: errors.New("failed to generate plan"),
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.Maven,
					Package:   "com.example:test",
					Version:   "1.0.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-error",
			},
			maxParallel:   1,
			expectedError: "failed to generate execution plan",
		},
		{
			name: "timeout handling",
			plan: &DockerRunPlan{
				Image:      "alpine:3.19",
				Command:    []string{"sleep", "10"},
				OutputPath: "/out/result.txt",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.Debian,
					Package:   "test-deb",
					Version:   "1.0.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-timeout",
				Timeout: 50 * time.Millisecond,
			},
			maxParallel: 1,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				select {
				case <-time.After(100 * time.Millisecond):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			expectSuccess: false,
		},
		{
			name: "with custom working directory",
			plan: &DockerRunPlan{
				Image:      "ubuntu:20.04",
				Command:    []string{"pwd"},
				OutputPath: "/out/result.txt",
				WorkingDir: "/custom/workdir",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "custom-pkg",
					Version:   "2.0.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-workdir",
			},
			maxParallel: 1,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if opts.Output != nil {
					opts.Output.Write([]byte("/custom/workdir\n"))
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name: "docker",
					Args: []string{"run", "--rm", "--name", "test-build-workdir", "-v", "/tmp/oss-rebuild-test-build-workdir:/out", "-w", "/custom/workdir", "ubuntu:20.04", "pwd"},
				},
			},
			expectSuccess: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock command executor
			cmdExecutor := NewMockCommandExecutor()
			if tc.executeFunc != nil {
				cmdExecutor.SetExecuteFunc(tc.executeFunc)
			}
			if tc.lookPathFunc != nil {
				cmdExecutor.SetLookPathFunc(tc.lookPathFunc)
			}
			executor, err := NewDockerRunExecutor(DockerRunExecutorConfig{
				Planner: &mockPlanner{
					plan: tc.plan,
					err:  tc.planError,
				},
				CommandExecutor: cmdExecutor,
				MaxParallel:     tc.maxParallel,
			})
			// Check constructor errors
			if tc.expectedError != "" && err != nil {
				if !strings.Contains(err.Error(), tc.expectedError) {
					t.Errorf("Expected error containing %q, got %q", tc.expectedError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error creating executor: %v", err)
			}
			// Test Status method
			status := executor.Status()
			expectedStatus := build.ExecutorStatus{
				InProgress: 0,
				Capacity:   tc.maxParallel,
				Healthy:    true,
			}
			if diff := cmp.Diff(expectedStatus, status); diff != "" {
				t.Errorf("Status mismatch (-want +got):\n%s", diff)
			}
			// Test Start method
			ctx := context.Background()
			handle, err := executor.Start(ctx, tc.input, tc.options)
			// Check for plan generation errors
			if tc.planError != nil {
				if err == nil {
					t.Fatal("Expected error from Start, got nil")
				}
				if !strings.Contains(err.Error(), tc.expectedError) {
					t.Errorf("Expected error containing %q, got %q", tc.expectedError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error from Start: %v", err)
			}
			// Wait for build to complete
			result, err := handle.Wait(ctx)
			// Check result
			if tc.expectSuccess && result.Error != nil {
				t.Errorf("Expected success, got error: %v", result.Error)
			}
			if !tc.expectSuccess && result.Error == nil {
				t.Error("Expected error, got success")
			}
			// Verify commands executed
			commands := cmdExecutor.GetCommands()
			if len(tc.expectedCommands) > 0 {
				if diff := cmp.Diff(tc.expectedCommands, commands, cmp.Comparer(func(e1 error, e2 error) bool {
					if e1 == nil || e2 == nil {
						return e1 == e2
					}
					return e1.Error() == e2.Error()
				})); diff != "" {
					t.Errorf("Command mismatch (-want +got):\n%s", diff)
				}
			}
			// Test Close method
			closeCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			if err := executor.Close(closeCtx); err != nil {
				t.Errorf("Unexpected error from Close: %v", err)
			}
		})
	}
}

func TestDockerRunExecutorConcurrency(t *testing.T) {
	maxParallel := 2
	cmdExecutor := NewMockCommandExecutor()
	// Setup slow execution to test concurrency
	var activeBuilds int32
	var maxActiveBuilds int32
	var mu sync.Mutex
	cmdExecutor.SetExecuteFunc(func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
		mu.Lock()
		activeBuilds++
		if activeBuilds > maxActiveBuilds {
			maxActiveBuilds = activeBuilds
		}
		mu.Unlock()
		// Simulate work
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		activeBuilds--
		mu.Unlock()
		if opts.Output != nil {
			opts.Output.Write([]byte("done\n"))
		}
		return nil
	})
	executor, err := NewDockerRunExecutor(DockerRunExecutorConfig{
		Planner: &mockPlanner{
			plan: &DockerRunPlan{
				Image:      "alpine:3.19",
				Command:    []string{"echo", "test"},
				OutputPath: "/out/result.txt",
			},
		},
		CommandExecutor: cmdExecutor,
		MaxParallel:     maxParallel,
	})
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}
	// Start multiple builds
	numBuilds := 5
	handles := make([]build.Handle, numBuilds)
	ctx := context.Background()
	for i := range numBuilds {
		input := rebuild.Input{
			Target: rebuild.Target{
				Ecosystem: rebuild.NPM,
				Package:   fmt.Sprintf("pkg-%d", i),
				Version:   "1.0.0",
			},
		}
		options := build.Options{
			BuildID: fmt.Sprintf("build-%d", i),
		}

		handle, err := executor.Start(ctx, input, options)
		if err != nil {
			t.Fatalf("Failed to start build %d: %v", i, err)
		}
		handles[i] = handle
	}
	// Wait for all builds to complete
	for i, handle := range handles {
		result, _ := handle.Wait(ctx)
		if result.Error != nil {
			t.Errorf("Build %d failed: %v", i, result.Error)
		}
	}
	// Verify concurrency was respected
	mu.Lock()
	finalMaxActive := maxActiveBuilds
	mu.Unlock()
	if finalMaxActive > int32(maxParallel) {
		t.Errorf("Concurrency limit exceeded: max active %d, limit %d", finalMaxActive, maxParallel)
	}
	// Cleanup
	closeCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	executor.Close(closeCtx)
}

func TestDockerRunExecutorConfig(t *testing.T) {
	executor, err := NewDockerRunExecutor(DockerRunExecutorConfig{
		MaxParallel: 3,
	})
	if err != nil {
		t.Fatalf("Failed to create executor with config: %v", err)
	}
	status := executor.Status()
	if status.Capacity != 3 {
		t.Errorf("Expected capacity 3, got %d", status.Capacity)
	}
}
