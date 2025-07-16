// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Executor implements build.Executor for Google Cloud Build execution using a planner
type Executor struct {
	planner            build.Planner[*Plan]
	client             gcb.Client
	project            string
	serviceAccount     string
	logsBucket         string
	privatePool        *gcb.PrivatePoolConfig
	outputBufferSize   int
	builderName        string
	terminateOnTimeout bool
	activeBuilds       syncx.Map[string, *gcbHandle]
}

// NewExecutor creates a new GCB executor with the specified configuration
func NewExecutor(config ExecutorConfig) (*Executor, error) {
	if config.LogsBucket == "" {
		return nil, errors.New("Logs configuration is required")
	}
	// Set defaults for unset config params
	planner := config.Planner
	if planner == nil {
		plannerConfig := PlannerConfig{
			Project:        config.Project,
			ServiceAccount: config.ServiceAccount,
			LogsBucket:     config.LogsBucket,
			PrivatePool:    config.PrivatePool,
		}
		planner = NewPlanner(plannerConfig)
	}
	outputBufferSize := config.OutputBufferSize
	if outputBufferSize <= 0 {
		outputBufferSize = 1024 * 1024 // 1MB default
	}
	return &Executor{
		planner:            planner,
		client:             config.Client,
		project:            config.Project,
		serviceAccount:     config.ServiceAccount,
		logsBucket:         config.LogsBucket,
		privatePool:        config.PrivatePool,
		outputBufferSize:   outputBufferSize,
		builderName:        config.BuilderName,
		terminateOnTimeout: config.TerminateOnTimeout,
		activeBuilds:       syncx.Map[string, *gcbHandle]{},
	}, nil
}

// ExecutorConfig contains configuration for creating a GCB executor
type ExecutorConfig struct {
	Planner            build.Planner[*Plan]
	Client             gcb.Client
	Project            string
	ServiceAccount     string
	LogsBucket         string
	PrivatePool        *gcb.PrivatePoolConfig
	OutputBufferSize   int
	BuilderName        string
	TerminateOnTimeout bool
}

// Start implements build.Executor
func (e *Executor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	buildID := opts.BuildID
	if buildID == "" {
		return nil, errors.New("must provide explicit BuildID")
	}
	// Generate the execution plan
	planOpts := build.PlanOptions{
		UseTimewarp:            opts.UseTimewarp,
		UseNetworkProxy:        opts.UseNetworkProxy,
		UseSyscallMonitor:      opts.UseSyscallMonitor,
		PreferPreciseToolchain: true,
		Resources:              opts.Resources,
	}
	plan, err := e.planner.GeneratePlan(ctx, input, planOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate execution plan")
	}
	// Make the Cloud Build "Build" request
	gcbBuild := e.makeBuild(plan)
	// Create a buffered pipe for streaming output
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(e.outputBufferSize))
	handle := &gcbHandle{
		id:           buildID,
		executor:     e,
		output:       pipe,
		outputChan:   make(chan string, 100),
		resultChan:   make(chan build.Result, 1),
		cancelPolicy: opts.CancelPolicy,
		status:       build.BuildStateStarting,
	}
	e.activeBuilds.Store(buildID, handle)
	// Start the build execution
	go e.executeBuild(ctx, handle, gcbBuild, input, opts, plan)
	return handle, nil
}

// Status implements build.Executor
func (e *Executor) Status() build.ExecutorStatus {
	activeCount := 0
	for range e.activeBuilds.Values() {
		activeCount++
	}
	return build.ExecutorStatus{
		InProgress: activeCount,
		Capacity:   -1, // TODO: Allow user to configure sizing
		Healthy:    true,
	}
}

// Close implements build.Executor
func (e *Executor) Close(ctx context.Context) error {
	// Cancel all active builds
	for handle := range e.activeBuilds.Values() {
		if handle.operation != nil {
			e.client.CancelOperation(handle.operation)
		}
		handle.updateStatus(build.BuildStateCancelled)
		handle.setResult(build.Result{Error: context.Canceled})
	}
	// Wait for builds to finish or context timeout
	done := make(chan struct{})
	go func() {
		for {
			activeCount := 0
			for range e.activeBuilds.Values() {
				activeCount++
			}
			if activeCount == 0 {
				break
			}
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

// executeBuild runs the actual build process using Cloud Build
func (e *Executor) executeBuild(ctx context.Context, handle *gcbHandle, cloudBuild *cloudbuild.Build, input rebuild.Input, opts build.Options, plan *Plan) {
	defer func() {
		e.activeBuilds.Delete(handle.id)
		handle.output.Close()
		close(handle.outputChan)
	}()
	// Construct BuildInfo to be incrementally set.
	buildInfo := rebuild.BuildInfo{
		Target:      input.Target,
		ObliviousID: opts.BuildID,
		Builder:     e.builderName,
		Steps:       plan.Steps,
	}
	// Submit and wait for the build
	buildResult, buildErr := e.doBuild(ctx, handle, cloudBuild)
	// Extract additional build information from GCB results if available
	if buildResult != nil {
		buildInfo.BuildID = buildResult.Id
		buildInfo.Steps = buildResult.Steps
		var err error
		buildInfo.BuildStart, err = time.Parse(time.RFC3339, buildResult.StartTime)
		if err != nil {
			buildErr = errors.Wrap(err, "parsing build start time")
		}
		buildInfo.BuildEnd, err = time.Parse(time.RFC3339, buildResult.FinishTime)
		if err != nil {
			buildErr = errors.Wrap(err, "parsing build end time")
		}
		if buildResult.Results != nil {
			buildInfo.BuildImages = make(map[string]string)
			for i, step := range buildInfo.Steps {
				if i < len(buildResult.Results.BuildStepImages) {
					buildInfo.BuildImages[step.Name] = buildResult.Results.BuildStepImages[i]
				}
			}
		}
	}
	// Upload assets to asset store if configured
	if opts.Resources.AssetStore != nil {
		if err := e.uploadContent(ctx, opts.Resources.AssetStore, rebuild.DockerfileAsset.For(input.Target), []byte(plan.Dockerfile)); err != nil {
			buildErr = errors.Wrap(err, "uploading Dockerfile")
		}
		if err := e.uploadBuildInfo(ctx, opts.Resources.AssetStore, rebuild.BuildInfoAsset.For(input.Target), buildInfo); err != nil {
			buildErr = errors.Wrap(err, "uploading build info")
		}
	}
	handle.updateStatus(build.BuildStateCompleted)
	handle.setResult(build.Result{
		Error: buildErr,
	})
}

// doBuild executes a build on Cloud Build, waits for completion and returns the resulting Build resource.
func (e *Executor) doBuild(ctx context.Context, handle *gcbHandle, cloudBuild *cloudbuild.Build) (*cloudbuild.Build, error) {
	createOp, err := e.client.CreateBuild(ctx, e.project, cloudBuild)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Cloud Build")
	}
	// TODO: Stream logs to handle
	handle.operation = createOp
	handle.updateStatus(build.BuildStateRunning)
	var createMetadata cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal(createOp.Metadata, &createMetadata); err != nil {
		return nil, errors.Wrap(err, "unmarshalling build metadata")
	}
	waitOp, err := e.client.WaitForOperation(ctx, createOp)
	if errors.Is(err, context.DeadlineExceeded) && e.terminateOnTimeout {
		log.Printf("GCB deadline exceeded, cancelling build %s", createOp.Name)
		if err := e.client.CancelOperation(createOp); err != nil {
			log.Printf("Best effort GCB cancellation failed: %v", err)
			return nil, errors.Wrap(err, "cancelling operation")
		}
		// We can wait 10 more seconds for operation to be updated
		newCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Wait for the now-cancelled op, then proceed normally.
		if waitOp, err = e.client.WaitForOperation(newCtx, createOp); err != nil {
			return nil, errors.Wrap(err, "fetching operation after cancel")
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("GCB deadline exceeded, allowing the build to continue")
		return createMetadata.Build, nil
	} else if err != nil {
		// NOTE: We could potentially also cancel these unknown error cases, not just DeadlineExceeded
		return nil, errors.Wrap(err, "waiting for operation")
	}
	// NOTE: Build status check will handle failures with better error messages.
	if waitOp.Error != nil {
		log.Printf("Cloud Build error: %v", status.Error(codes.Code(waitOp.Error.Code), waitOp.Error.Message))
	}
	// Extract build information from the operation metadata
	var waitMetadata cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal(waitOp.Metadata, &waitMetadata); err != nil {
		return nil, errors.Wrap(err, "unmarshalling build metadata")
	}
	return waitMetadata.Build, nil
}

// makeBuild constructs a cloudbuild.Build from plan and executor config
func (e *Executor) makeBuild(plan *Plan) *cloudbuild.Build {
	buildOptions := &cloudbuild.BuildOptions{
		Logging: "GCS_ONLY",
	}
	if e.privatePool != nil {
		buildOptions.Pool = &cloudbuild.PoolOption{
			Name: e.privatePool.Name,
		}
	}
	return &cloudbuild.Build{
		Steps:          plan.Steps,
		Options:        buildOptions,
		LogsBucket:     e.logsBucket,
		ServiceAccount: e.serviceAccount,
	}
}

// uploadBuildInfo uploads BuildInfo as JSON to the asset store
func (e *Executor) uploadBuildInfo(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, buildInfo rebuild.BuildInfo) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(buildInfo); err != nil {
		return errors.Wrap(err, "failed to encode and write build info")
	}
	return nil
}

// uploadContent uploads content directly to the asset store
func (e *Executor) uploadContent(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, content []byte) error {
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
