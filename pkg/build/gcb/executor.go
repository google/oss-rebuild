// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/timing"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var buildTagDisallowedChar = regexp.MustCompile(`[^\w.-]`)

// Executor implements build.Executor for Google Cloud Build execution using a planner
type Executor struct {
	planner            build.Planner[*Plan]
	client             gcb.Client
	logsClient         gcb.LogsClient
	project            string
	serviceAccount     string
	logsBucket         string
	privatePool        *gcb.PrivatePoolConfig
	jumboPool          *gcb.PrivatePoolConfig
	outputBufferSize   int
	builderName        string
	terminateOnTimeout bool
	extraTags          map[string]string
	activeBuilds       syncx.Map[string, *gcbHandle]
}

// GCSLogsClientFunc func creates a LogsClient for a given bucket.
type GCSLogsClientFunc func(bucket string) gcb.LogsClient

func GCSLogsClient(gcsClient *storage.Client) GCSLogsClientFunc {
	return func(bucket string) gcb.LogsClient {
		return gcb.NewGCSLogsClient(gcsClient, bucket)
	}
}

// NewExecutor creates a new GCB executor with the specified configuration
func NewExecutor(config ExecutorConfig) (*Executor, error) {
	if config.LogsBucket == "" || config.LogsClientFunc == nil {
		return nil, errors.New("Logs configuration is required")
	}
	// Set defaults for unset config params
	planner := config.Planner
	if planner == nil {
		plannerConfig := PlannerConfig{
			ServiceAccount: config.ServiceAccount,
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
		logsClient:         config.LogsClientFunc(config.LogsBucket),
		project:            config.Project,
		serviceAccount:     config.ServiceAccount,
		logsBucket:         config.LogsBucket,
		privatePool:        config.PrivatePool,
		jumboPool:          config.JumboPool,
		outputBufferSize:   outputBufferSize,
		builderName:        config.BuilderName,
		terminateOnTimeout: config.TerminateOnTimeout,
		extraTags:          config.ExtraTags,
		activeBuilds:       syncx.Map[string, *gcbHandle]{},
	}, nil
}

// ExecutorConfig contains configuration for creating a GCB executor
type ExecutorConfig struct {
	Planner            build.Planner[*Plan]
	Client             gcb.Client
	LogsClientFunc     GCSLogsClientFunc
	Project            string
	ServiceAccount     string
	LogsBucket         string
	PrivatePool        *gcb.PrivatePoolConfig
	JumboPool          *gcb.PrivatePoolConfig
	OutputBufferSize   int
	BuilderName        string
	TerminateOnTimeout bool
	ExtraTags          map[string]string
}

// Start implements build.Executor
func (e *Executor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	buildID := opts.BuildID
	if buildID == "" {
		return nil, errors.New("must provide explicit BuildID")
	}
	// Generate the execution plan
	planOpts := build.PlanOptions{
		UseTimewarp:       opts.UseTimewarp,
		UseNetworkProxy:   opts.UseNetworkProxy,
		UseSyscallMonitor: opts.UseSyscallMonitor,
		Resources:         opts.Resources,
		RecordTimings:     opts.RecordTimings,
	}
	plan, err := e.planner.GeneratePlan(ctx, input, planOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate execution plan")
	}
	// Make the Cloud Build "Build" request
	var gcbBuild *cloudbuild.Build
	{
		var pool *cloudbuild.PoolOption
		if opts.SizeHint == schema.JumboSize && e.jumboPool != nil {
			pool = &cloudbuild.PoolOption{
				Name: e.jumboPool.Name,
			}
		} else if e.privatePool != nil {
			pool = &cloudbuild.PoolOption{
				Name: e.privatePool.Name,
			}
		}
		var timeout time.Duration
		if opts.Timeout > 0 {
			timeout = opts.Timeout
		} else if pool != nil {
			// Force GCB to extend the timeout beyond 60 minutes for private pools, close to the maximum of 24 hours
			timeout = 23 * time.Hour
		} else {
			// Maximum timeout for public pool is 60 minutes
			timeout = 55 * time.Minute
		}
		tags := []string{
			buildTag("ecosystem", string(input.Target.Ecosystem)),
			buildTag("package", input.Target.Package),
			buildTag("version", input.Target.Version),
		}
		for k, v := range e.extraTags {
			tags = append(tags, buildTag(k, v))
		}
		gcbBuild = &cloudbuild.Build{
			Steps: plan.Steps,
			Options: &cloudbuild.BuildOptions{
				Logging: "GCS_ONLY",
				Pool:    pool,
			},
			LogsBucket:     e.logsBucket,
			ServiceAccount: e.serviceAccount,
			Tags:           tags,
			Timeout:        fmt.Sprintf("%ds", int(timeout.Seconds())),
		}
	}
	// Create a buffered pipe for streaming output
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(e.outputBufferSize))
	handle := &gcbHandle{
		id:           buildID,
		executor:     e,
		output:       pipe,
		resultChan:   make(chan build.Result, 1),
		cancelPolicy: opts.CancelPolicy,
		status:       build.BuildStateStarting,
	}
	e.activeBuilds.Store(buildID, handle)
	// Start the build execution
	go e.executeBuild(ctx, handle, gcbBuild, input.Target, opts, plan)
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
func (e *Executor) executeBuild(ctx context.Context, handle *gcbHandle, cloudBuild *cloudbuild.Build, t rebuild.Target, opts build.Options, plan *Plan) {
	// Construct BuildInfo to be incrementally set.
	buildInfo := rebuild.BuildInfo{
		Target:      t,
		ObliviousID: opts.BuildID,
		Builder:     e.builderName,
		Steps:       plan.Steps,
	}
	// Submit and wait for the build
	buildResult, buildErr := e.doBuild(ctx, handle, cloudBuild)
	// If the build itself failed, that will be stored in buildResult.Status
	if buildErr == nil {
		buildErr = gcb.ToError(buildResult)
	}
	// Extract additional build information from GCB results if available
	if buildResult != nil {
		buildInfo.BuildID = buildResult.Id
		buildInfo.Steps = buildResult.Steps
		var err error
		buildInfo.BuildStart, err = time.Parse(time.RFC3339, buildResult.StartTime)
		if buildErr == nil && err != nil {
			buildErr = errors.Wrap(err, "parsing build start time")
		}
		buildInfo.BuildEnd, err = time.Parse(time.RFC3339, buildResult.FinishTime)
		if buildErr == nil && err != nil {
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
	// Extract phase timings from post-build observation.
	// NOTE: Extraction failures are logged, never surfaced as build errors.
	// NOTE: The timing step only runs after a clean main step or one that
	// exited with the allowed run-failure sentinel. Any other failure
	// (preamble, image build, timeout) aborts before it, leaving no record.
	var timings *rebuild.BuildTimings
	runFailed := stepExitCode(buildResult, 0) == runFailureExitCode
	if runFailed && buildErr == nil {
		buildErr = errors.Errorf("GCB build run step failed with exit code %d", runFailureExitCode)
	}
	if (buildErr == nil || runFailed) && buildInfo.BuildID != "" && plan.timingStep() >= 0 {
		var err error
		timings, err = e.extractTimings(ctx, buildInfo.BuildID, plan, buildResult, runFailed)
		if err != nil {
			log.Printf("Build %s timing extraction failed: %v", buildInfo.BuildID, err)
		}
	}
	// Upload assets to asset store if configured
	if opts.Resources.AssetStore != nil {
		if err := e.uploadContent(ctx, opts.Resources.AssetStore, rebuild.DockerfileAsset.For(t), []byte(plan.Dockerfile)); err != nil {
			buildErr = errors.Wrap(err, "uploading Dockerfile")
		}
		if err := e.uploadBuildInfo(ctx, opts.Resources.AssetStore, rebuild.BuildInfoAsset.For(t), buildInfo); err != nil {
			buildErr = errors.Wrap(err, "uploading build info")
		}
		if buildInfo.BuildID != "" {
			if err := e.copyBuildLogs(ctx, opts.Resources.AssetStore, rebuild.DebugLogsAsset.For(t), buildInfo.BuildID); err != nil {
				buildErr = errors.Wrap(err, "uploading build logs")
			}
		}
	}
	handle.updateStatus(build.BuildStateCompleted)
	// NOTE: Delete before setResult so handle.Wait returns *after* executor considers it retired.
	e.activeBuilds.Delete(handle.id)
	handle.setResult(build.Result{
		Error:   buildErr,
		Timings: timings,
	})
}

// extractTimings assembles phase timings from the timing step's inspect and
// history output. A sentinel-failed run yields a marked record: the image
// phases completed (the image exists), and the container span runs until the
// script's termination. An unusable container span leaves Build unmeasured
// without discarding the image phases.
// NOTE: The step's section is filtered out of the shared build log by line
// prefix, so a line torn by a monitored-mode writer at worst drops that line.
func (e *Executor) extractTimings(ctx context.Context, buildID string, plan *Plan, buildResult *cloudbuild.Build, runFailed bool) (*rebuild.BuildTimings, error) {
	r, err := e.logsClient.ReadStepLogs(ctx, buildID, plan.timingStep())
	if err != nil {
		return nil, errors.Wrap(err, "opening timing step logs")
	}
	defer r.Close()
	section, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrap(err, "reading timing step logs")
	}
	layers, err := timing.ParseHistory(section, plan.Layers)
	if err != nil {
		return nil, errors.Wrap(err, "parsing image history")
	}
	buildStart, err := stepStart(buildResult, 0)
	if err != nil {
		return nil, errors.Wrap(err, "reading main step start")
	}
	setup, source, deps, err := layers.Phases(buildStart)
	if err != nil {
		return nil, errors.Wrap(err, "deriving phase durations")
	}
	in := rebuild.BuildTimings{Setup: &setup, Source: &source, Deps: &deps}
	if runFailed {
		in.FailedIn = rebuild.PhaseBuild
	}
	if buildDur, err := timing.ContainerSpan(section); err != nil {
		log.Printf("Build %s container span unreadable: %v", buildID, err)
	} else if buildDur <= 0 {
		log.Printf("Build %s container span non-positive: %v", buildID, buildDur)
	} else {
		in.Build = &buildDur
	}
	return timing.Validated(in), nil
}

func stepStart(b *cloudbuild.Build, i int) (time.Time, error) {
	if b == nil || i >= len(b.Steps) || b.Steps[i] == nil || b.Steps[i].Timing == nil {
		return time.Time{}, errors.New("step timing unavailable")
	}
	return time.Parse(time.RFC3339, b.Steps[i].Timing.StartTime)
}

// stepExitCode reads a step's recorded exit code, populated by GCB only for
// steps whose failure was allowed. Zero when unavailable.
func stepExitCode(b *cloudbuild.Build, i int) int64 {
	if b == nil || i >= len(b.Steps) || b.Steps[i] == nil {
		return 0
	}
	return b.Steps[i].ExitCode
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
	if errors.Is(err, context.DeadlineExceeded) && e.terminateOnTimeout || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.Canceled) {
			log.Printf("Context cancelled, cancelling build %s", createOp.Name)
		} else {
			log.Printf("GCB deadline exceeded, cancelling build %s", createOp.Name)
		}
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

func buildTag(desc, val string) string {
	tag := buildTagDisallowedChar.ReplaceAllString(fmt.Sprintf("%s-%s", desc, val), "")
	return tag[:min(len(tag), 127)]
}

// uploadBuildInfo uploads BuildInfo as JSON to the asset store
func (e *Executor) uploadBuildInfo(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, buildInfo rebuild.BuildInfo) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	if err := json.NewEncoder(writer).Encode(buildInfo); err != nil {
		writer.Close()
		return errors.Wrap(err, "failed to encode and write build info")
	}
	return errors.Wrap(writer.Close(), "committing asset")
}

// uploadContent uploads content directly to the asset store
func (e *Executor) uploadContent(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, content []byte) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	if _, err := writer.Write(content); err != nil {
		writer.Close()
		return errors.Wrap(err, "failed to write content to asset store")
	}
	return errors.Wrap(writer.Close(), "committing asset")
}

// copyBuildLogs copies build logs using the logs client to the asset store.
// If the asset type is not supported by the store, the copy is skipped.
func (e *Executor) copyBuildLogs(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, buildID string) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		if errors.Is(err, rebuild.ErrAssetTypeNotSupported) {
			return nil
		}
		return errors.Wrap(err, "creating asset writer")
	}
	defer writer.Close()
	reader, err := e.logsClient.ReadBuildLogs(ctx, buildID)
	if err != nil {
		return errors.Wrap(err, "reading build logs")
	}
	defer reader.Close()
	if _, err := io.Copy(writer, reader); err != nil {
		return errors.Wrap(err, "copying to asset")
	}
	return errors.Wrap(writer.Close(), "committing asset")
}
