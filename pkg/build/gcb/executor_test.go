// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
)

func TestGCBExecutorStart(t *testing.T) {
	ctx := context.Background()

	// Create mock client
	mockClient := &gcbtest.MockClient{}

	// Setup mock responses
	buildResult := &cloudbuild.Build{
		Id:         "test-build-gcb-123",
		Status:     "SUCCESS",
		StartTime:  time.Now().Format(time.RFC3339),
		FinishTime: time.Now().Format(time.RFC3339),
		Steps: []*cloudbuild.BuildStep{
			{
				Name: "gcr.io/cloud-builders/docker",
				Args: []string{"build", "-t", "test-image", "."},
			},
		},
		Results: &cloudbuild.Results{
			BuildStepImages: []string{"gcr.io/test-project/test-image:latest"},
		},
	}
	// Create operation metadata
	metadata := &cloudbuild.BuildOperationMetadata{
		Build: buildResult,
	}
	metadataBytes, _ := json.Marshal(metadata)
	operation := &cloudbuild.Operation{
		Name:     "projects/test-project/operations/test-build-123",
		Metadata: googleapi.RawMessage(metadataBytes),
	}
	doneOperation := &cloudbuild.Operation{
		Name:     "projects/test-project/operations/test-build-123",
		Done:     true,
		Metadata: googleapi.RawMessage(metadataBytes),
	}
	createChan := make(chan struct{})
	defer close(createChan)
	mockClient.CreateBuildFunc = func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
		<-createChan
		return operation, nil
	}
	mockClient.WaitForOperationFunc = func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
		return doneOperation, nil
	}

	// Create executor
	config := ExecutorConfig{
		Client:           mockClient,
		Project:          "test-project",
		ServiceAccount:   "test@test.iam.gserviceaccount.com",
		LogsBucket:       "test-bucket",
		OutputBufferSize: 1024,
	}

	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewGCBExecutor failed: %v", err)
	}

	// Create test input
	input := rebuild.Input{
		Target: rebuild.Target{
			Ecosystem: rebuild.NPM,
			Package:   "test-package",
			Version:   "1.0.0",
			Artifact:  "test-package-1.0.0.tgz",
		},
		Strategy: &rebuild.ManualStrategy{
			Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
			SystemDeps: []string{"git", "node", "npm"},
			Deps:       "npm install",
			Build:      "npm run build",
			OutputPath: "dist/test-package-1.0.0.tgz",
		},
	}

	baseImageConfig := build.BaseImageConfig{
		Default: "docker.io/library/alpine:3.19",
	}

	opts := build.Options{
		BuildID:         "test-build-123",
		UseTimewarp:     false,
		UseNetworkProxy: false,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
		},
	}

	// Test Start method
	handle, err := executor.Start(ctx, input, opts)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if handle == nil {
		t.Fatal("Handle should not be nil")
	}
	if handle.BuildID() != "test-build-123" {
		t.Errorf("Expected BuildID 'test-build-123', got '%s'", handle.BuildID())
	}
	outputStream := handle.OutputStream()
	if outputStream == nil {
		t.Error("OutputStream should not be nil")
	}
	if handle.Status() != build.BuildStateStarting {
		t.Errorf("Expected initial status %v, got %v", build.BuildStateStarting, handle.Status())
	}
	createChan <- struct{}{} // let create complete
	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != nil {
		t.Fatal(result.Error)
	}

	// Clean up
	if err := executor.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestGCBExecutorStatus(t *testing.T) {
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

	status := executor.Status()

	if status.InProgress != 0 {
		t.Errorf("Expected 0 builds in progress, got %d", status.InProgress)
	}

	if status.Capacity != -1 {
		t.Errorf("Expected unlimited capacity (-1), got %d", status.Capacity)
	}

	if !status.Healthy {
		t.Error("Executor should be healthy")
	}
}

func TestGCBExecutorClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

	err = executor.Close(ctx)
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestGCBExecutorWithSyscallMonitor(t *testing.T) {
	ctx := context.Background()

	mockClient := &gcbtest.MockClient{}
	operation := &cloudbuild.Operation{
		Name: "projects/test-project/operations/test-build-123",
	}
	mockClient.CreateBuildFunc = func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
		return operation, nil
	}
	mockClient.WaitForOperationFunc = func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
		return &cloudbuild.Operation{
			Name: "projects/test-project/operations/test-build-123",
			Done: true,
		}, nil
	}
	mockClient.CancelOperationFunc = func(op *cloudbuild.Operation) error { return nil }

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

	input := rebuild.Input{
		Target: rebuild.Target{
			Ecosystem: rebuild.NPM,
			Package:   "test-package",
			Version:   "1.0.0",
			Artifact:  "test-package-1.0.0.tgz",
		},
		Strategy: &rebuild.ManualStrategy{
			Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
			SystemDeps: []string{"git", "node", "npm"},
			Deps:       "npm install",
			Build:      "npm run build",
			OutputPath: "dist/test-package-1.0.0.tgz",
		},
	}

	baseImageConfig := build.BaseImageConfig{
		Default: "docker.io/library/alpine:3.19",
	}

	opts := build.Options{
		BuildID:           "test-build-123",
		UseTimewarp:       false,
		UseNetworkProxy:   false,
		UseSyscallMonitor: true,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
		},
	}

	handle, err := executor.Start(ctx, input, opts)
	if err != nil {
		t.Fatalf("Start with syscall monitor failed: %v", err)
	}

	if handle == nil {
		t.Fatal("Handle should not be nil")
	}

	// Verify that the Cloud Build contains syscall monitoring
	// We can't easily inspect the build details with the current mock structure,
	// but we can verify that the handle was created successfully and that
	// the syscall monitor option was properly processed.
	if handle.BuildID() != "test-build-123" {
		t.Errorf("Expected BuildID 'test-build-123', got '%s'", handle.BuildID())
	}
	// Wait for build to finish
	if _, err := handle.Wait(ctx); err != nil {
		t.Fatalf("Waiting for completion failed: %v", err)
	}
	// Clean up
	if err := executor.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestGCBExecutorAssetUpload(t *testing.T) {
	ctx := context.Background()
	// Create mock asset store
	fsStore := rebuild.NewFilesystemAssetStore(memfs.New())
	// Create mock GCB client with detailed responses
	mockClient := &gcbtest.MockClient{}
	buildResult := &cloudbuild.Build{
		Id:         "test-build-gcb-123",
		Status:     "SUCCESS",
		StartTime:  time.Now().Format(time.RFC3339),
		FinishTime: time.Now().Format(time.RFC3339),
		Steps: []*cloudbuild.BuildStep{
			{
				Name: "gcr.io/cloud-builders/docker",
				Args: []string{"build", "-t", "test-image", "."},
			},
		},
		Results: &cloudbuild.Results{
			BuildStepImages: []string{"gcr.io/test-project/test-image:latest"},
		},
	}
	// Create operation metadata
	metadata := &cloudbuild.BuildOperationMetadata{
		Build: buildResult,
	}
	metadataBytes, _ := json.Marshal(metadata)
	operation := &cloudbuild.Operation{
		Name:     "projects/test-project/operations/test-build-123",
		Metadata: googleapi.RawMessage(metadataBytes),
	}
	doneOperation := &cloudbuild.Operation{
		Name:     "projects/test-project/operations/test-build-123",
		Done:     true,
		Metadata: googleapi.RawMessage(metadataBytes),
	}
	mockClient.CreateBuildFunc = func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
		return operation, nil
	}
	mockClient.WaitForOperationFunc = func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
		return doneOperation, nil
	}
	// Create executor with Builder parameter
	config := ExecutorConfig{
		Client:           mockClient,
		Project:          "test-project",
		ServiceAccount:   "test@test.iam.gserviceaccount.com",
		LogsBucket:       "test-bucket",
		OutputBufferSize: 1024,
		BuilderName:      "test-k-revision-123",
	}
	executor, err := NewExecutor(config)
	if err != nil {
		t.Fatalf("NewExecutor failed: %v", err)
	}
	// Create test input
	input := rebuild.Input{
		Target: rebuild.Target{
			Ecosystem: rebuild.NPM,
			Package:   "test-package",
			Version:   "1.0.0",
			Artifact:  "test-package-1.0.0.tgz",
		},
		Strategy: &rebuild.ManualStrategy{
			Location:   rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
			SystemDeps: []string{"git", "node", "npm"},
			Deps:       "npm install",
			Build:      "npm run build",
			OutputPath: "dist/test-package-1.0.0.tgz",
		},
	}
	baseImageConfig := build.BaseImageConfig{
		Default: "docker.io/library/alpine:3.19",
	}
	opts := build.Options{
		BuildID: "test-build-123",
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
			AssetStore:      fsStore,
		},
	}
	// Start the build
	handle, err := executor.Start(ctx, input, opts)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	// Wait for build completion
	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("Build failed: %v", result.Error)
	}
	// Verify BuildInfo asset was uploaded
	buildInfoData := must(io.ReadAll(must(fsStore.Reader(ctx, rebuild.BuildInfoAsset.For(input.Target)))))
	if len(buildInfoData) == 0 {
		t.Error("BuildInfo asset was not uploaded")
	} else {
		var buildInfo rebuild.BuildInfo
		if err := json.Unmarshal(buildInfoData, &buildInfo); err != nil {
			t.Errorf("Failed to unmarshal BuildInfo: %v", err)
		} else {
			if buildInfo.Target != input.Target {
				t.Errorf("BuildInfo target mismatch: expected %v, got %v", input.Target, buildInfo.Target)
			}
			if buildInfo.Builder != "test-k-revision-123" {
				t.Errorf("BuildInfo builder mismatch: expected %s, got %s", "test-k-revision-123", buildInfo.Builder)
			}
			if buildInfo.BuildID != "test-build-gcb-123" {
				t.Errorf("BuildInfo BuildID mismatch: expected %s, got %s", "test-build-gcb-123", buildInfo.BuildID)
			}
			if len(buildInfo.Steps) == 0 {
				t.Error("BuildInfo steps are empty")
			}
			if len(buildInfo.BuildImages) == 0 {
				t.Error("BuildInfo BuildImages are empty")
			}
		}
	}
	// Verify Dockerfile asset was uploaded
	dockerfileData := must(io.ReadAll(must(fsStore.Reader(ctx, rebuild.DockerfileAsset.For(input.Target)))))
	if len(dockerfileData) == 0 {
		t.Error("Dockerfile asset was not uploaded")
	} else {
		dockerfileContent := string(dockerfileData)
		if !strings.Contains(dockerfileContent, "FROM") {
			t.Error("Dockerfile content doesn't contain expected FROM instruction")
		}
	}
	// Clean up
	if err := executor.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
