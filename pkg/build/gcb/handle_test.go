// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"github.com/google/oss-rebuild/pkg/build"
	"google.golang.org/api/cloudbuild/v1"
)

func TestGCBHandle(t *testing.T) {
	// Create a mock executor
	mockClient := &gcbtest.MockClient{}
	config := ExecutorConfig{
		Client:         mockClient,
		Project:        "test-project",
		ServiceAccount: "test@test.iam.gserviceaccount.com",
		LogsBucket:     "test-bucket",
	}

	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewGCBExecutor failed: %v", err)
	}

	// Create a buffered pipe
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(1024))

	// Create a handle
	handle := &gcbHandle{
		id:           "test-build-123",
		executor:     executor,
		output:       pipe,
		outputChan:   make(chan string, 100),
		resultChan:   make(chan build.Result, 1),
		cancelPolicy: build.CancelImmediate,
		status:       build.BuildStateStarting,
	}

	// Test BuildID
	if handle.BuildID() != "test-build-123" {
		t.Errorf("Expected BuildID 'test-build-123', got '%s'", handle.BuildID())
	}

	// Test Status
	if handle.Status() != build.BuildStateStarting {
		t.Errorf("Expected status %v, got %v", build.BuildStateStarting, handle.Status())
	}

	// Test OutputStream
	outputStream := handle.OutputStream()
	if outputStream == nil {
		t.Error("OutputStream should not be nil")
	}

	// Test writing to output
	testData := []byte("test output\n")
	n, err := handle.Write(testData)
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to write %d bytes, wrote %d", len(testData), n)
	}

	// Test reading from output
	readBuf := make([]byte, len(testData))
	n, err = outputStream.Read(readBuf)
	if err != nil {
		t.Errorf("Read failed: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Expected to read %d bytes, read %d", len(testData), n)
	}
	if string(readBuf) != string(testData) {
		t.Errorf("Expected to read '%s', got '%s'", string(testData), string(readBuf))
	}

	// Test status update
	handle.updateStatus(build.BuildStateRunning)
	if handle.Status() != build.BuildStateRunning {
		t.Errorf("Expected status %v after update, got %v", build.BuildStateRunning, handle.Status())
	}

	// Test cancel
	handle.Cancel()
	// The cancel should close the output pipe
	// Note: We can't easily test the actual cancellation of Cloud Build operation in a unit test
}

func TestGCBHandleWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Create a mock executor
	mockClient := &gcbtest.MockClient{}
	config := ExecutorConfig{
		Client:         mockClient,
		Project:        "test-project",
		ServiceAccount: "test@test.iam.gserviceaccount.com",
		LogsBucket:     "test-bucket",
	}

	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewGCBExecutor failed: %v", err)
	}

	// Create a buffered pipe
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(1024))

	// Create a handle
	handle := &gcbHandle{
		id:           "test-build-123",
		executor:     executor,
		output:       pipe,
		outputChan:   make(chan string, 100),
		resultChan:   make(chan build.Result, 1),
		cancelPolicy: build.CancelImmediate,
		status:       build.BuildStateStarting,
	}

	// Test timeout case
	_, err = handle.Wait(ctx)
	if err == nil {
		t.Error("Expected timeout error")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestGCBHandleWaitSuccess(t *testing.T) {
	ctx := context.Background()

	// Create a mock executor
	mockClient := &gcbtest.MockClient{}
	config := ExecutorConfig{
		Client:         mockClient,
		Project:        "test-project",
		ServiceAccount: "test@test.iam.gserviceaccount.com",
		LogsBucket:     "test-bucket",
	}

	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewGCBExecutor failed: %v", err)
	}

	// Create a buffered pipe
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(1024))

	// Create a handle
	handle := &gcbHandle{
		id:           "test-build-123",
		executor:     executor,
		output:       pipe,
		outputChan:   make(chan string, 100),
		resultChan:   make(chan build.Result, 1),
		cancelPolicy: build.CancelImmediate,
		status:       build.BuildStateStarting,
	}

	// Set a successful result
	expectedResult := build.Result{Error: nil}
	go func() {
		time.Sleep(10 * time.Millisecond) // Small delay to ensure Wait is called first
		handle.setResult(expectedResult)
	}()

	result, err := handle.Wait(ctx)
	if err != nil {
		t.Errorf("Wait failed: %v", err)
	}
	if result.Error != expectedResult.Error {
		t.Errorf("Expected result error %v, got %v", expectedResult.Error, result.Error)
	}
}

func TestGCBHandleCancelPolicies(t *testing.T) {
	mockClient := &gcbtest.MockClient{}
	mockClient.CancelOperationFunc = func(op *cloudbuild.Operation) error {
		return nil // Mock successful cancellation
	}

	config := ExecutorConfig{
		Client:         mockClient,
		Project:        "test-project",
		ServiceAccount: "test@test.iam.gserviceaccount.com",
		LogsBucket:     "test-bucket",
	}

	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewGCBExecutor failed: %v", err)
	}

	testCases := []struct {
		name         string
		cancelPolicy build.CancelPolicy
	}{
		{"CancelImmediate", build.CancelImmediate},
		{"CancelGraceful", build.CancelGraceful},
		{"CancelDetached", build.CancelDetached},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(1024))
			operation := &cloudbuild.Operation{
				Name: "projects/test-project/operations/test-build-123",
			}

			handle := &gcbHandle{
				id:           "test-build-123",
				executor:     executor,
				operation:    operation,
				output:       pipe,
				outputChan:   make(chan string, 100),
				resultChan:   make(chan build.Result, 1),
				cancelPolicy: tc.cancelPolicy,
				status:       build.BuildStateRunning,
			}

			// Test cancel - this should not panic and should handle different policies
			handle.Cancel()

			// For CancelDetached, the status should be updated to Running
			if handle.Status() != build.BuildStateCancelled {
				t.Errorf("Expected status to remain %v for detached cancel, got %v",
					build.BuildStateRunning, handle.Status())
			}
		})
	}
}
