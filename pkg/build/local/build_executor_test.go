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
type mockBuildPlanner struct {
	plan *DockerBuildPlan
	err  error
}

func (m *mockBuildPlanner) GeneratePlan(ctx context.Context, input rebuild.Input, opts build.PlanOptions) (*DockerBuildPlan, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.plan, nil
}

func newMockBuildAssetStore() rebuild.LocatableAssetStore {
	return rebuild.NewFilesystemAssetStore(memfs.New())
}

// Test case structure
type buildTestCase struct {
	name             string
	plan             *DockerBuildPlan
	planError        error
	input            rebuild.Input
	options          build.Options
	maxParallel      int
	executeFunc      func(ctx context.Context, opts CommandOptions, name string, args ...string) error
	lookPathFunc     func(file string) (string, error)
	expectedCommands []MockCommand
	expectedError    string
	expectSuccess    bool
	retainContainer  bool
	retainImage      bool
}

func TestDockerBuildExecutor(t *testing.T) {
	testCases := []buildTestCase{
		{
			name: "successful execution",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN echo 'building'\nCMD echo 'done'",
				OutputPath: "/out/result.tar.gz",
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
					AssetStore: newMockBuildAssetStore(),
				},
			},
			maxParallel: 2,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if opts.Output != nil {
					if len(args) > 0 && args[0] == "build" {
						opts.Output.Write([]byte("Successfully built image\n"))
					} else if len(args) > 0 && args[0] == "run" {
						opts.Output.Write([]byte("Container executed successfully\n"))
					}
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"buildx", "build", "-t", "test-build-123", "-"},
					Input: "FROM alpine:3.19\nRUN echo 'building'\nCMD echo 'done'",
				},
				{
					Name: "docker",
					Args: []string{"run", "--rm", "-v", "/tmp/oss-rebuild-test-build-123:/out", "test-build-123"},
				},
				{
					Name: "docker",
					Args: []string{"save", "-o", "/tmp/oss-rebuild-test-build-123/image.tgz", "test-build-123"},
				},
				{
					Name: "docker",
					Args: []string{"rmi", "test-build-123"},
				},
			},
			expectSuccess: true,
		},
		{
			name: "docker command not found",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN echo 'building'",
				OutputPath: "/out/result.tar.gz",
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
			name: "docker build failure",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM nonexistent:image\nRUN false",
				OutputPath: "/out/result.tar.gz",
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
				if len(args) > 0 && args[0] == "buildx" {
					return errors.New("docker build failed: exit status 1")
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"buildx", "build", "-t", "test-build-789", "-"},
					Input: "FROM nonexistent:image\nRUN false",
					Error: errors.New("docker build failed: exit status 1"),
				},
			},
			expectSuccess: false,
		},
		{
			name: "docker run failure",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nCMD false",
				OutputPath: "/out/result.tar.gz",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.Maven,
					Package:   "com.example:test",
					Version:   "1.0.0",
				},
			},
			options: build.Options{
				BuildID: "test-build-run-fail",
			},
			maxParallel: 1,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if len(args) > 0 && args[0] == "run" {
					return errors.New("container exited with code 1")
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"buildx", "build", "-t", "test-build-run-fail", "-"},
					Input: "FROM alpine:3.19\nCMD false",
				},
				{
					Name:  "docker",
					Args:  []string{"run", "--rm", "-v", "/tmp/oss-rebuild-test-build-run-fail:/out", "test-build-run-fail"},
					Error: errors.New("container exited with code 1"),
				},
				{
					Name: "docker",
					Args: []string{"rmi", "test-build-run-fail"},
				},
			},
			expectSuccess: false,
		},
		{
			name: "retain container functionality",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN echo 'building with retain container'",
				OutputPath: "/out/result.tar.gz",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-pkg-retain",
					Version:   "1.0.0",
					Artifact:  "test-pkg-retain-1.0.0.tgz",
				},
			},
			options: build.Options{
				BuildID: "test-build-retain-container",
				Resources: build.Resources{
					AssetStore: newMockBuildAssetStore(),
				},
			},
			maxParallel:     1,
			retainContainer: true,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if opts.Output != nil {
					if len(args) > 0 && args[0] == "build" {
						opts.Output.Write([]byte("Successfully built image\n"))
					} else if len(args) > 0 && args[0] == "run" {
						opts.Output.Write([]byte("Container executed successfully\n"))
					}
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"buildx", "build", "-t", "test-build-retain-container", "-"},
					Input: "FROM alpine:3.19\nRUN echo 'building with retain container'",
				},
				{
					Name: "docker",
					Args: []string{"run", "-v", "/tmp/oss-rebuild-test-build-retain-container:/out", "test-build-retain-container"},
				},
				{
					Name: "docker",
					Args: []string{"save", "-o", "/tmp/oss-rebuild-test-build-retain-container/image.tgz", "test-build-retain-container"},
				},
				{
					Name: "docker",
					Args: []string{"rmi", "test-build-retain-container"},
				},
			},
			expectSuccess: true,
		},
		{
			name: "retain image functionality",
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN echo 'building with retain image'",
				OutputPath: "/out/result.tar.gz",
			},
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-pkg-retain-image",
					Version:   "1.0.0",
					Artifact:  "test-pkg-retain-image-1.0.0.tgz",
				},
			},
			options: build.Options{
				BuildID: "test-build-retain-image",
				Resources: build.Resources{
					AssetStore: newMockBuildAssetStore(),
				},
			},
			maxParallel: 1,
			retainImage: true,
			executeFunc: func(ctx context.Context, opts CommandOptions, name string, args ...string) error {
				if opts.Output != nil {
					if len(args) > 0 && args[0] == "build" {
						opts.Output.Write([]byte("Successfully built image\n"))
					} else if len(args) > 0 && args[0] == "run" {
						opts.Output.Write([]byte("Container executed successfully\n"))
					}
				}
				return nil
			},
			expectedCommands: []MockCommand{
				{
					Name:  "docker",
					Args:  []string{"buildx", "build", "-t", "test-build-retain-image", "-"},
					Input: "FROM alpine:3.19\nRUN echo 'building with retain image'",
				},
				{
					Name: "docker",
					Args: []string{"run", "--rm", "-v", "/tmp/oss-rebuild-test-build-retain-image:/out", "test-build-retain-image"},
				},
				{
					Name: "docker",
					Args: []string{"save", "-o", "/tmp/oss-rebuild-test-build-retain-image/image.tgz", "test-build-retain-image"},
				},
			},
			expectSuccess: true,
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
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN sleep 10",
				OutputPath: "/out/result.tar.gz",
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

			executor, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{
				Planner: &mockBuildPlanner{
					plan: tc.plan,
					err:  tc.planError,
				},
				CommandExecutor: cmdExecutor,
				MaxParallel:     tc.maxParallel,
				OutputDir:       "/tmp",
				RetainContainer: tc.retainContainer,
				RetainImage:     tc.retainImage,
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
				// For failure cases, do exact comparison
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

func TestDockerBuildExecutorConcurrency(t *testing.T) {
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
			if len(args) > 0 && args[0] == "build" {
				opts.Output.Write([]byte("Built successfully\n"))
			} else if len(args) > 0 && args[0] == "run" {
				opts.Output.Write([]byte("Run completed\n"))
			}
		}
		return nil
	})

	executor, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{
		Planner: &mockBuildPlanner{
			plan: &DockerBuildPlan{
				Dockerfile: "FROM alpine:3.19\nRUN echo 'test'",
				OutputPath: "/out/result.tar.gz",
			},
		},
		CommandExecutor: cmdExecutor,
		MaxParallel:     maxParallel,
		OutputDir:       "/tmp",
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

func TestDockerBuildExecutorConfig(t *testing.T) {
	executor, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{
		MaxParallel:     3,
		OutputDir:       "/custom/output",
		CommandExecutor: NewMockCommandExecutor(),
	})
	if err != nil {
		t.Fatalf("Failed to create executor with config: %v", err)
	}

	status := executor.Status()
	if status.Capacity != 3 {
		t.Errorf("Expected capacity 3, got %d", status.Capacity)
	}
}
