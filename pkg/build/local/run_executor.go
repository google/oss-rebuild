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
	"time"

	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

const defaultOutputBufferSize = 512 * 1024 // 512KB

// DockerRunExecutor implements build.Executor for local Docker run execution using a planner
type DockerRunExecutor struct {
	planner          build.Planner[*DockerRunPlan]
	maxParallel      int
	semaphore        chan struct{}
	dockerCmd        string
	cmdExecutor      CommandExecutor
	activeBuilds     syncx.Map[string, *localHandle]
	outputBufferSize int
	retainContainer  bool
	tempDirBase      string
}

// NewDockerRunExecutor creates a new Docker run executor with configuration
func NewDockerRunExecutor(config DockerRunExecutorConfig) (*DockerRunExecutor, error) {
	// Set defaults for unset config params
	planner := config.Planner
	if planner == nil {
		planner = NewDockerRunPlanner()
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
	tempBase := config.TempDirBase
	if tempBase == "" {
		tempBase = os.TempDir()
	}
	// Check if docker is available
	dockerCmd := "docker"
	if _, err := cmdExecutor.LookPath(dockerCmd); err != nil {
		return nil, errors.Wrap(err, "docker command not found")
	}
	return &DockerRunExecutor{
		planner:          planner,
		maxParallel:      maxParallel,
		semaphore:        make(chan struct{}, maxParallel),
		dockerCmd:        dockerCmd,
		cmdExecutor:      cmdExecutor,
		activeBuilds:     syncx.Map[string, *localHandle]{},
		outputBufferSize: outputBufferSize,
		retainContainer:  config.RetainContainer,
		tempDirBase:      tempBase,
	}, nil
}

// DockerRunExecutorConfig contains configuration for creating a Docker run executor
type DockerRunExecutorConfig struct {
	Planner          build.Planner[*DockerRunPlan]
	CommandExecutor  CommandExecutor
	MaxParallel      int    // Max number of simultaneous builds
	OutputBufferSize int    // Buffer size for output pipe, defaults to 512KB
	RetainContainer  bool   // If true, don't use --rm flag to retain containers
	TempDirBase      string // Base directory for temp files, if empty uses os.TempDir()
}

// Start implements build.Executor
func (e *DockerRunExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	buildID := opts.BuildID
	if buildID == "" {
		buildID = fmt.Sprintf("docker-run-%d", time.Now().UnixNano())
	}
	planOpts := build.PlanOptions{
		UseTimewarp:            opts.UseTimewarp,
		UseNetworkProxy:        opts.UseNetworkProxy,
		UseSyscallMonitor:      opts.UseSyscallMonitor,
		PreferPreciseToolchain: true, // NOTE: Only false in smoketest
		Resources:              opts.Resources,
	}
	plan, err := e.planner.GeneratePlan(ctx, input, planOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate execution plan")
	}
	buildCtx, cancel := context.WithCancel(context.Background())
	if opts.Timeout > 0 {
		buildCtx, cancel = context.WithTimeout(buildCtx, opts.Timeout)
	}
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(e.outputBufferSize))
	handle := &localHandle{
		id:         buildID,
		cancel:     cancel,
		output:     pipe,
		resultChan: make(chan build.Result, 1),
		status:     build.BuildStateStarting,
	}
	e.activeBuilds.Store(buildID, handle)
	// Start the build in a goroutine
	go e.executeBuild(buildCtx, handle, plan, input, opts)
	return handle, nil
}

// Status implements build.Executor
func (e *DockerRunExecutor) Status() build.ExecutorStatus {
	return build.ExecutorStatus{
		InProgress: len(e.semaphore),
		Capacity:   e.maxParallel,
		Healthy:    true,
	}
}

// Close implements build.Executor
func (e *DockerRunExecutor) Close(ctx context.Context) error {
	// Cancel all active builds
	for handle := range e.activeBuilds.Values() {
		handle.cancel()
		handle.updateStatus(build.BuildStateCancelled)
	}
	// Wait for builds to finish or context timeout
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

// executeBuild runs the actual Docker run process
func (e *DockerRunExecutor) executeBuild(ctx context.Context, handle *localHandle, plan *DockerRunPlan, input rebuild.Input, opts build.Options) {
	defer e.activeBuilds.Delete(handle.id)
	defer handle.output.Close()
	// Acquire semaphore slot
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
	// Create temporary directory for build output
	hostOutputPath := filepath.Join(e.tempDirBase, fmt.Sprintf("oss-rebuild-%s", handle.id))
	err := os.MkdirAll(hostOutputPath, 0755)
	if err != nil {
		handle.updateStatus(build.BuildStateCancelled)
		handle.setResult(build.Result{
			Error: errors.Wrap(err, "failed to create temporary directory"),
		})
		return
	}
	defer func() {
		if err := os.RemoveAll(hostOutputPath); err != nil {
			log.Printf("Failed to clean up temporary directory %s: %v", hostOutputPath, err)
		}
	}()
	// Compose command args
	runArgs := []string{"run"}
	if !e.retainContainer {
		runArgs = append(runArgs, "--rm")
	}
	runArgs = append(runArgs, "--name", handle.id) // Use BuildID as container name
	runArgs = append(runArgs, "-v", fmt.Sprintf("%s:%s", hostOutputPath, path.Dir(plan.OutputPath)))
	if plan.WorkingDir != "" {
		runArgs = append(runArgs, "-w", plan.WorkingDir)
	}
	runArgs = append(runArgs, plan.Image)
	runArgs = append(runArgs, "/bin/sh", "-c", plan.Script)
	// Execute the Docker run command with streaming output
	handle.updateStatus(build.BuildStateRunning)
	outbuf := &bytes.Buffer{}
	runWriter := io.MultiWriter(handle, outbuf)
	buildErr := e.cmdExecutor.Execute(ctx, CommandOptions{
		Output: runWriter,
	}, e.dockerCmd, runArgs...)
	// Upload assets to asset store
	// NOTE: Upload failures don't fail the build
	if opts.Resources.AssetStore != nil {
		rebuildArtifactPath := filepath.Join(hostOutputPath, path.Base(plan.OutputPath))
		if _, err := os.Stat(rebuildArtifactPath); err != nil {
			log.Printf("Failed to stat rebuild artifact: %v", err)
		} else if err := e.uploadFile(ctx, opts.Resources.AssetStore, rebuild.RebuildAsset.For(input.Target), rebuildArtifactPath); err != nil {
			log.Printf("Failed to upload rebuild artifact: %v", err)
		}
		if err := e.uploadContent(ctx, opts.Resources.AssetStore, rebuild.DebugLogsAsset.For(input.Target), []byte(outbuf.String())); err != nil {
			log.Printf("Failed to upload debug logs: %v", err)
		}
	}
	handle.updateStatus(build.BuildStateCompleted)
	handle.setResult(build.Result{
		Error: errors.Wrap(buildErr, "docker run failed"),
	})
}

// uploadFile uploads a local file to the asset store
func (e *DockerRunExecutor) uploadFile(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, filePath string) error {
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

// uploadContent uploads content directly to the asset store
func (e *DockerRunExecutor) uploadContent(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, content []byte) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	if _, err := writer.Write(content); err != nil {
		return errors.Wrap(err, "failed to write content to asset store")
	}
	return nil
}
