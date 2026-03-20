// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// DockerBuildExecutor implements build.Executor for local Docker build execution using a planner.
type DockerBuildExecutor struct {
	planner          build.Planner[*DockerBuildPlan]
	maxParallel      int
	semaphore        chan struct{}
	dockerCmd        string
	outputDir        string
	cmdExecutor      CommandExecutor
	activeBuilds     syncx.Map[string, *localHandle]
	outputBufferSize int
	retainContainer  bool
	retainImage      bool
	tempDirBase      string
	allowPrivileged  bool
}

// NewDockerBuildExecutor creates a new Docker build executor with configuration
func NewDockerBuildExecutor(config DockerBuildExecutorConfig) (*DockerBuildExecutor, error) {
	// Set defaults for unset config params
	planner := config.Planner
	if planner == nil {
		planner = NewDockerBuildPlanner()
	}
	cmdExecutor := config.CommandExecutor
	if cmdExecutor == nil {
		cmdExecutor = NewRealCommandExecutor()
	}
	maxParallel := config.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 1 // Default to 1 if not set
	}
	outputBufferSize := config.OutputBufferSize
	if outputBufferSize <= 0 {
		outputBufferSize = defaultOutputBufferSize
	}
	outputDir := config.OutputDir
	if outputDir == "" {
		outputDir = "/tmp/oss-rebuild"
	}
	tempBase := config.TempDirBase
	if tempBase == "" {
		tempBase = os.TempDir()
	}
	// Check if docker is available
	dockerCmd := "docker"
	if _, err := cmdExecutor.LookPath(dockerCmd); err != nil {
		return nil, errors.Wrap(err, "docker command not found")
	}
	return &DockerBuildExecutor{
		planner:          planner,
		maxParallel:      maxParallel,
		semaphore:        make(chan struct{}, maxParallel),
		dockerCmd:        dockerCmd,
		outputDir:        outputDir,
		cmdExecutor:      cmdExecutor,
		activeBuilds:     syncx.Map[string, *localHandle]{},
		outputBufferSize: outputBufferSize,
		retainContainer:  config.RetainContainer,
		retainImage:      config.RetainImage,
		tempDirBase:      tempBase,
		allowPrivileged:  config.AllowPrivileged,
	}, nil
}

// DockerBuildExecutorConfig contains configuration for creating a Docker build executor
type DockerBuildExecutorConfig struct {
	Planner          build.Planner[*DockerBuildPlan]
	CommandExecutor  CommandExecutor
	MaxParallel      int    // Max number of simultaneous builds
	OutputDir        string // Directory for build outputs
	OutputBufferSize int    // Buffer size for output pipe, defaults to 512KB
	RetainContainer  bool   // If true, don't use --rm flag to retain containers
	RetainImage      bool   // If true, don't remove built image after build completes
	TempDirBase      string // Base directory for temp files, if empty uses os.TempDir()
	AllowPrivileged  bool   // If true, allow privileged builds
}

// Start implements build.Executor.
func (e *DockerBuildExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	// buildID is used as image name and must be lowercase
	buildID := strings.ToLower(opts.BuildID)
	if buildID == "" {
		buildID = fmt.Sprintf("docker-build-%d", time.Now().UnixNano())
	}
	planOpts := build.PlanOptions{
		UseTimewarp:        opts.UseTimewarp,
		UseNetworkProxy:    opts.UseNetworkProxy,
		UseSyscallMonitor:  opts.UseSyscallMonitor,
		Resources:          opts.Resources,
		SaveContainerImage: opts.SaveContainerImage,
	}
	plan, err := e.planner.GeneratePlan(ctx, input, planOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate execution plan")
	}
	// Create build context that can be cancelled independently.
	buildCtx, cancel := context.WithCancel(context.Background())
	if opts.Timeout > 0 {
		buildCtx, cancel = context.WithTimeout(buildCtx, opts.Timeout)
	}
	// Create a buffered pipe for streaming output.
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(e.outputBufferSize))
	handle := &localHandle{
		id:         buildID,
		cancel:     cancel,
		output:     pipe,
		resultChan: make(chan build.Result, 1),
		status:     build.BuildStateStarting,
	}
	e.activeBuilds.Store(buildID, handle)
	// Start the build in a goroutine.
	go e.executeBuild(buildCtx, handle, plan, input.Target, opts)
	return handle, nil
}

// Status implements build.Executor.
func (e *DockerBuildExecutor) Status() build.ExecutorStatus {
	return build.ExecutorStatus{
		InProgress: len(e.semaphore),
		Capacity:   e.maxParallel,
		Healthy:    true,
	}
}

// Close implements build.Executor.
func (e *DockerBuildExecutor) Close(ctx context.Context) error {
	// Cancel all active builds.
	for handle := range e.activeBuilds.Values() {
		handle.cancel()
		handle.updateStatus(build.BuildStateCancelled)
	}
	// Wait for builds to finish or context timeout.
	done := make(chan struct{})
	go func() {
		for len(e.semaphore) > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "closing executor")
	}
}

// executeBuild runs the actual Docker build process.
func (e *DockerBuildExecutor) executeBuild(ctx context.Context, handle *localHandle, plan *DockerBuildPlan, t rebuild.Target, opts build.Options) {
	// Ensure resources are cleaned up on exit.
	defer e.activeBuilds.Delete(handle.id)
	defer handle.output.Close()
	// Acquire semaphore slot.
	select {
	case e.semaphore <- struct{}{}:
		defer func() { <-e.semaphore }()
	case <-ctx.Done():
		handle.updateStatus(build.BuildStateCancelled)
		handle.setResult(build.Result{
			Error: errors.Wrap(ctx.Err(), "enqueuing build"),
		})
		return
	}
	// Generate host output path and create the directory.
	hostOutputPath := filepath.Join(e.tempDirBase, fmt.Sprintf("oss-rebuild-%s", handle.id))
	if err := os.MkdirAll(hostOutputPath, 0755); err != nil {
		handle.updateStatus(build.BuildStateCancelled)
		handle.setResult(build.Result{
			Error: errors.Wrap(err, "failed to create output directory"),
		})
		return
	}
	handle.updateStatus(build.BuildStateRunning)
	// Create a buffer to capture all output for asset upload.
	outbuf := &bytes.Buffer{}
	// Create a multi-writer to stream to the handle's output and capture to the buffer.
	multiWriter := io.MultiWriter(handle.output, outbuf)
	// Build Docker image with streaming and captured output.
	imageTag := handle.id
	buildArgs := []string{"buildx", "build", "-t", imageTag, "-"}
	err := e.cmdExecutor.Execute(ctx, CommandOptions{
		Input:  strings.NewReader(plan.Dockerfile),
		Output: multiWriter,
	}, e.dockerCmd, buildArgs...)
	if err != nil {
		handle.updateStatus(build.BuildStateCompleted)
		handle.setResult(build.Result{
			Error: errors.Wrap(err, "docker build failed"),
		})
		return
	}
	// Run Docker container with streaming and captured output.
	runArgs := []string{"run"}
	if !e.retainContainer {
		runArgs = append(runArgs, "--rm")
	}
	runArgs = append(runArgs, "-v", fmt.Sprintf("%s:%s", hostOutputPath, path.Dir(plan.OutputPath)), imageTag)
	if plan.Privileged {
		if e.allowPrivileged {
			runArgs = append(runArgs, "--privileged")
		} else {
			log.Println("Warning: plan requested privileged execution but this executor does not allow privileged builds.")
		}
	}
	err = e.cmdExecutor.Execute(ctx, CommandOptions{
		Output: multiWriter,
	}, e.dockerCmd, runArgs...)
	// Upload assets to asset store
	if opts.Resources.AssetStore != nil {
		e.uploadAssets(ctx, plan, hostOutputPath, t, opts, handle.id, outbuf.Bytes())
	}
	// Clean up the built image if RetainImage is false
	if !e.retainImage {
		if rmErr := e.cmdExecutor.Execute(ctx, CommandOptions{}, e.dockerCmd, "rmi", imageTag); rmErr != nil {
			// Log the error but don't fail the build
			log.Printf("Failed to remove Docker image %s: %v", imageTag, rmErr)
		}
	}
	// If the run command failed, set the result with the error.
	if err != nil {
		handle.updateStatus(build.BuildStateCompleted)
		handle.setResult(build.Result{
			Error: errors.Wrap(err, "docker run failed"),
		})
		return
	}
	// Set final successful result.
	handle.updateStatus(build.BuildStateCompleted)
	handle.setResult(build.Result{Error: nil})
}

// uploadAssets uploads build artifacts to the asset store.
func (e *DockerBuildExecutor) uploadAssets(ctx context.Context, plan *DockerBuildPlan, hostOutputPath string, t rebuild.Target, opts build.Options, imageTag string, logs []byte) {
	store := opts.Resources.AssetStore
	if store == nil {
		log.Println("No asset store configured. Skipping asset upload.")
		return
	}
	// Upload rebuild artifact if it exists.
	rebuildArtifactPath := filepath.Join(hostOutputPath, filepath.Base(plan.OutputPath))
	if _, err := os.Stat(rebuildArtifactPath); err != nil {
		log.Printf("Failed to stat rebuild artifact: %v", err)
	} else if err := e.uploadFile(ctx, store, rebuild.RebuildAsset.For(t), rebuildArtifactPath); err != nil {
		log.Printf("Failed to upload rebuild artifact: %v", err)
	}
	// Upload Dockerfile.
	if err := e.uploadContent(ctx, store, rebuild.DockerfileAsset.For(t), []byte(plan.Dockerfile)); err != nil {
		log.Printf("Failed to upload Dockerfile: %v", err)
	}
	// Upload combined build and run logs.
	if err := e.uploadContent(ctx, store, rebuild.DebugLogsAsset.For(t), logs); err != nil {
		log.Printf("Failed to upload build logs: %v", err)
	}
	// Save and upload container image if requested.
	if opts.SaveContainerImage {
		imagePath := filepath.Join(hostOutputPath, string(rebuild.ContainerImageAsset))
		if err := e.saveContainerImage(ctx, imageTag, imagePath); err != nil {
			log.Printf("Failed to save container image: %v", err)
		} else if err := e.uploadFile(ctx, store, rebuild.ContainerImageAsset.For(t), imagePath); err != nil {
			log.Printf("Failed to upload container image: %v", err)
		}
	}
}

// uploadFile uploads a local file to the asset store.
func (e *DockerBuildExecutor) uploadFile(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return errors.Wrapf(err, "failed to open file %s", filePath)
	}
	defer file.Close()
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	if _, err := io.Copy(writer, file); err != nil {
		return errors.Wrap(err, "failed to upload file to asset store")
	}
	return nil
}

// uploadContent uploads content directly to the asset store.
func (e *DockerBuildExecutor) uploadContent(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, content []byte) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	if _, err := writer.Write(content); err != nil {
		return errors.Wrap(err, "failed to write to asset store")
	}
	return nil
}

// saveContainerImage saves the built container image as a gzipped tarball.
func (e *DockerBuildExecutor) saveContainerImage(ctx context.Context, imageTag, outputPath string) error {
	return e.cmdExecutor.Execute(ctx, CommandOptions{}, "sh", "-c",
		fmt.Sprintf("%s save %s | gzip > %s", e.dockerCmd, imageTag, outputPath))
}
