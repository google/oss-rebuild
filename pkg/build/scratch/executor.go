// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"path"
	"regexp"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/bufiox"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

const (
	defaultOutputBufferSize = 512 * 1024
	// defaultWorkDir matches the scratch worker's default working directory.
	defaultWorkDir = "/home/builder"
	// buildsSubdir is the directory under WorkDir holding per-build state.
	buildsSubdir = "builds"
	// defaultBuildTimeout bounds a build when Options.Timeout is unset. The
	// worker-enforced timeout must never be zero: with no kill endpoint an
	// unbounded remote command could not be terminated.
	defaultBuildTimeout = time.Hour
	// utilityTimeout bounds the non-build exec steps (staging, artifact
	// retrieval).
	utilityTimeout = 10 * time.Minute
	// defaultMaxArtifactBytes caps artifact retrieval, which round-trips
	// base64 over the exec output channel.
	defaultMaxArtifactBytes = 256 << 20
	// Sentinel exit codes for the artifact retrieval script.
	exitNoArtifact     = 44
	exitArtifactTooBig = 45
)

// ErrNoArtifact is returned via build.Result.Error when the build exits
// successfully but leaves no artifact at the plan's output path.
var ErrNoArtifact = errors.New("build produced no artifact at the output path")

// ExitError is returned via build.Result.Error when a build phase exits
// nonzero. Callers distinguish build failures from infrastructure failures
// with errors.As.
type ExitError struct {
	Code int
	// Phase is the build phase that exited ("setup", "source", "deps",
	// "build").
	Phase string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("build failed in %s phase with exit code %d", e.Phase, e.Code)
}

// buildIDPattern constrains build IDs to docker-name- and path-safe strings
// since the ID is used as a container name and staging directory component.
var buildIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ExecutorConfig configures a scratch Executor.
type ExecutorConfig struct {
	ScratchID        string                                // Scratch VM used, allocated and torn down by the session owner
	Stubs            Stubs                                 // Agent-api scratch exec endpoints
	GCSClient        *gcs.Client                           // Reads exec output objects
	WorkDir          string                                // Abspath to the writable VM directory, defaults to /home/builder
	Planner          build.Planner[*local.DockerRunPlan]   // Defaults to local.NewDockerRunPlanner()
	MaxParallel      int                                   // Max concurrent builds, defaults to 1
	OutputBufferSize int                                   // Buffer size for the output pipe, defaults to 512KB
	PollInterval     time.Duration                         // Steady-state gap between exec op polls
	DefaultTimeout   time.Duration                         // Bounds builds whose Options.Timeout is unset, defaults to 1h
	AuthHeader       func(context.Context) (string, error) // Supplies AUTH_HEADER for plans that fetch auth-required tools
	AllowPrivileged  bool                                  // If true, allow privileged builds
	RetainContainer  bool                                  // If true, retain build containers stopped and don't remove them
	MaxArtifactBytes int64                                 // Caps artifact retrieval, defaults to 256MiB
}

// Executor implements build.Executor on a scratch VM via the scratch exec
// API. It follows the same handle/lifecycle conventions as the local and GCB
// executors.
//
// NOTE: On cancellation, there is no worker kill endpoint yet, so cancelling a
// build (handle cancel, Close) stops driving it client-side while the remote
// command runs on until its worker-enforced timeout (build.CancelDetached
// semantics). Every dispatched exec therefore carries a nonzero timeout.
type Executor struct {
	scratchID        string
	stubs            Stubs
	gcsClient        *gcs.Client
	workDir          string
	planner          build.Planner[*local.DockerRunPlan]
	maxParallel      int
	semaphore        chan struct{}
	outputBufferSize int
	pollInterval     time.Duration
	defaultTimeout   time.Duration
	authHeader       func(context.Context) (string, error)
	allowPrivileged  bool
	retainContainer  bool
	maxArtifactBytes int64
	activeBuilds     syncx.Map[string, *scratchHandle]
}

var _ build.Executor = (*Executor)(nil)

// NewExecutor creates a scratch executor from config.
func NewExecutor(config ExecutorConfig) (*Executor, error) {
	if config.ScratchID == "" {
		return nil, errors.New("ScratchID is required")
	}
	if config.Stubs.ExecCreate == nil || config.Stubs.ExecGet == nil {
		return nil, errors.New("exec stubs are required")
	}
	if config.GCSClient == nil {
		return nil, errors.New("GCSClient is required")
	}
	planner := config.Planner
	if planner == nil {
		planner = local.NewDockerRunPlanner()
	}
	workDir := cmp.Or(config.WorkDir, defaultWorkDir)
	if !path.IsAbs(workDir) {
		return nil, errors.Errorf("WorkDir must be absolute, got %q", workDir)
	}
	maxParallel := max(config.MaxParallel, 1)
	return &Executor{
		scratchID:        config.ScratchID,
		stubs:            config.Stubs,
		gcsClient:        config.GCSClient,
		workDir:          workDir,
		planner:          planner,
		maxParallel:      maxParallel,
		semaphore:        make(chan struct{}, maxParallel),
		outputBufferSize: cmp.Or(config.OutputBufferSize, defaultOutputBufferSize),
		pollInterval:     config.PollInterval,
		defaultTimeout:   cmp.Or(config.DefaultTimeout, defaultBuildTimeout),
		authHeader:       config.AuthHeader,
		allowPrivileged:  config.AllowPrivileged,
		retainContainer:  config.RetainContainer,
		maxArtifactBytes: cmp.Or(config.MaxArtifactBytes, defaultMaxArtifactBytes),
		activeBuilds:     syncx.Map[string, *scratchHandle]{},
	}, nil
}

// Start implements build.Executor.
func (e *Executor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	// Container image assets would round-trip hundreds of MB through the
	// exec output channel. Reject rather than degrade.
	if opts.SaveContainerImage {
		return nil, errors.New("SaveContainerImage is not supported by the scratch executor")
	}
	if opts.SavePostBuildContainer {
		return nil, errors.New("SavePostBuildContainer is not supported by the scratch executor")
	}
	buildID := opts.BuildID
	if buildID == "" {
		buildID = fmt.Sprintf("scratch-%d", time.Now().UnixNano())
	}
	if !buildIDPattern.MatchString(buildID) {
		return nil, errors.Errorf("invalid build ID %q", buildID)
	}
	planOpts := build.PlanOptions{
		UseTimewarp:       opts.UseTimewarp,
		UseNetworkProxy:   opts.UseNetworkProxy,
		UseSyscallMonitor: opts.UseSyscallMonitor,
		Resources:         opts.Resources,
	}
	plan, err := e.planner.GeneratePlan(ctx, input, planOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate execution plan")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = e.defaultTimeout
	}
	// The build context is detached from ctx (like other executors) and
	// bounded by the exec poll loop's internal backstop.
	buildCtx, cancel := context.WithCancel(context.Background())
	pipe := bufiox.NewBufferedPipe(bufiox.NewLineBuffer(e.outputBufferSize))
	handle := &scratchHandle{
		id:         buildID,
		cancel:     cancel,
		output:     pipe,
		resultChan: make(chan build.Result, 1),
		status:     build.BuildStateStarting,
	}
	e.activeBuilds.Store(buildID, handle)
	go e.executeBuild(buildCtx, handle, plan, input.Target, opts, timeout)
	return handle, nil
}

// Status implements build.Executor.
func (e *Executor) Status() build.ExecutorStatus {
	return build.ExecutorStatus{
		InProgress: len(e.semaphore),
		Capacity:   e.maxParallel,
		Healthy:    true,
	}
}

// Close implements build.Executor.
func (e *Executor) Close(ctx context.Context) error {
	for handle := range e.activeBuilds.Values() {
		handle.Cancel()
	}
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

// executeBuild acquires a build slot, runs runBuild, and finalizes the
// handle exactly once: Cancelled when a failure coincides with build context
// cancellation, Completed otherwise.
func (e *Executor) executeBuild(ctx context.Context, handle *scratchHandle, plan *local.DockerRunPlan, t rebuild.Target, opts build.Options, timeout time.Duration) {
	defer e.activeBuilds.Delete(handle.id)
	defer handle.output.Close()
	select {
	case e.semaphore <- struct{}{}:
		defer func() { <-e.semaphore }()
	case <-ctx.Done():
		handle.updateStatus(build.BuildStateCancelled)
		handle.setResult(build.Result{Error: errors.Wrap(ctx.Err(), "enqueuing build")})
		return
	}
	err := e.runBuild(ctx, handle, plan, t, opts, timeout)
	if err != nil && ctx.Err() != nil {
		handle.updateStatus(build.BuildStateCancelled)
	} else {
		handle.updateStatus(build.BuildStateCompleted)
	}
	handle.setResult(build.Result{Error: err})
}

// runBuild drives one build on the scratch VM. The container is stopped and,
// unless RetainContainer is set, removed after the phases. The staging
// directory is always retained for post-mortem inspection.
func (e *Executor) runBuild(ctx context.Context, handle *scratchHandle, plan *local.DockerRunPlan, t rebuild.Target, opts build.Options, timeout time.Duration) error {
	dir := path.Join(e.workDir, buildsSubdir, handle.id)
	container := "rb-" + handle.id
	prepareScript := strings.Join([]string{
		"set -eu",
		fmt.Sprintf("mkdir -p %q", dir+"/out"),
		"docker ps -aq --filter name=^rb- | xargs -r docker rm -f",
	}, "\n")
	if err := e.utilityExec(ctx, []string{"/bin/sh", "-c", prepareScript}, nil, "preparing build"); err != nil {
		return err
	}
	argOpts := local.RunArgsOpts{
		ContainerName:   container,
		OutputMountSrc:  dir + "/out",
		Remove:          !e.retainContainer,
		AllowPrivileged: e.allowPrivileged,
	}
	if plan.Privileged && !e.allowPrivileged {
		log.Println("Warning: plan requested privileged execution but this executor does not allow privileged builds.")
	}
	var env map[string]string
	if plan.RequiresAuth {
		if e.authHeader == nil {
			return errors.New("build plan requires auth but no auth header source is configured")
		}
		header, err := e.authHeader(ctx)
		if err != nil {
			return errors.Wrap(err, "generating auth header")
		}
		env = map[string]string{"AUTH_HEADER": header}
		argOpts.AuthMode = local.AuthEnvPassthrough
	}
	if err := e.utilityExec(ctx, append([]string{"docker"}, local.ComposeDockerStartArgs(plan, argOpts)...), env, "starting build container"); err != nil {
		return err
	}
	// The phases share one wall-clock budget. Each op's worker-enforced
	// timeout is clamped to at least 1s: zero means unbounded, and nothing
	// could kill the command (see the cancellation caveat).
	handle.updateStatus(build.BuildStateRunning)
	var phaseOps []*longrunning.Operation[schema.ScratchExecResult]
	var buildErr error
	remaining := timeout
	for _, ph := range plan.Phases() {
		var offset int64
		follow := func(op *longrunning.Operation[schema.ScratchExecResult]) {
			offset += e.copyNewOutput(ctx, op, offset, handle)
		}
		phaseStart := time.Now()
		op, err := exec(ctx, e.stubs, schema.ScratchExecRequest{
			ScratchID:      e.scratchID,
			Cmd:            append([]string{"docker"}, local.ComposeDockerExecArgs(container, ph)...),
			TimeoutSeconds: max(1, int(remaining.Seconds())),
		}, e.pollInterval, follow)
		remaining -= time.Since(phaseStart)
		if err == nil {
			phaseOps = append(phaseOps, op)
		}
		if buildErr = phaseOutcome(ph.Name, op, err); buildErr != nil {
			break
		}
	}
	e.stopContainer(ctx, container)
	e.uploadDebugLogs(ctx, handle.id, phaseOps, t, opts.Resources.AssetStore)
	if buildErr != nil {
		return buildErr
	}
	// Retrieve and upload the artifact. Unlike the local executor, a
	// missing artifact or failed upload fails the build.
	if opts.Resources.AssetStore != nil {
		return e.fetchAndUploadArtifact(ctx, dir, plan, t, opts.Resources.AssetStore)
	}
	return nil
}

// utilityExec runs one short exec op, folding all failure channels into a
// single error wrapped with what.
func (e *Executor) utilityExec(ctx context.Context, cmd []string, env map[string]string, what string) error {
	op, err := Exec(ctx, e.stubs, schema.ScratchExecRequest{
		ScratchID:      e.scratchID,
		Cmd:            cmd,
		Env:            env,
		TimeoutSeconds: int(utilityTimeout.Seconds()),
	}, e.pollInterval)
	if err != nil {
		return errors.Wrap(err, what)
	}
	if op.Error != nil {
		return errors.Wrap(op.Error, what)
	}
	if op.Result.ExitCode != 0 {
		return errors.Errorf("%s failed with exit code %d", what, op.Result.ExitCode)
	}
	return nil
}

// phaseOutcome reduces one phase exec's outcome to an error. A nil op is
// valid only alongside a non-nil err.
func phaseOutcome(name string, op *longrunning.Operation[schema.ScratchExecResult], err error) error {
	if err != nil {
		return errors.Wrapf(err, "executing %s phase", name)
	}
	if op.Error != nil {
		if op.Error.Code == int(codes.DeadlineExceeded) {
			return errors.Wrapf(context.DeadlineExceeded, "%s phase timed out", name)
		}
		return errors.Wrapf(op.Error, "%s phase exec lost", name)
	}
	if op.Result.ExitCode != 0 {
		return &ExitError{Code: op.Result.ExitCode, Phase: name}
	}
	return nil
}

// stopContainer halts the build container's idle init once the phases are
// done, letting --rm reclaim it. Best effort under a fresh context so it
// runs on cancellation. The next build's prepare sweep covers missed stops.
func (e *Executor) stopContainer(ctx context.Context, container string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	if err := e.utilityExec(sctx, []string{"docker", "stop", "-t", "0", container}, nil, "stopping build container"); err != nil {
		log.Printf("build container %s: %v", container, err)
	}
}

// uploadDebugLogs streams the concatenated phase outputs into the asset
// store. Upload failures never fail the build.
func (e *Executor) uploadDebugLogs(ctx context.Context, buildID string, phaseOps []*longrunning.Operation[schema.ScratchExecResult], t rebuild.Target, store rebuild.AssetStore) {
	if store == nil {
		return
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	var readers []io.Reader
	var closers []io.Closer
	for _, phaseOp := range phaseOps {
		if rd, err := e.outputReader(uctx, phaseOp); err != nil {
			log.Printf("build %s: reading debug logs: %v", buildID, err)
		} else {
			readers = append(readers, rd)
			closers = append(closers, rd)
		}
	}
	if err := e.uploadStream(uctx, store, rebuild.DebugLogsAsset.For(t), io.MultiReader(readers...)); err != nil {
		log.Printf("build %s: uploading debug logs: %v", buildID, err)
	}
	for _, c := range closers {
		c.Close()
	}
}

// copyNewOutput copies bytes past offset from the op's output object into w,
// returning the number of bytes copied. Best effort: errors are logged and
// retried implicitly on the next poll (the output buffer is append-only).
func (e *Executor) copyNewOutput(ctx context.Context, op *longrunning.Operation[schema.ScratchExecResult], offset int64, w io.Writer) int64 {
	obj, err := outputObject(e.gcsClient, op)
	if err != nil || obj == nil {
		return 0
	}
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if !errors.Is(err, gcs.ErrObjectNotExist) {
			log.Printf("exec %s: stat output: %v", op.ID, err)
		}
		return 0
	}
	if attrs.Size <= offset {
		return 0
	}
	rd, err := obj.NewRangeReader(ctx, offset, -1)
	if err != nil {
		log.Printf("exec %s: read output: %v", op.ID, err)
		return 0
	}
	defer rd.Close()
	// A partial copy is fine: the next poll resumes from the new offset.
	n, err := io.Copy(w, rd)
	if err != nil {
		log.Printf("exec %s: copy output: %v", op.ID, err)
	}
	return n
}

// fetchAndUploadArtifact retrieves the built artifact from the VM by base64
// over the exec output channel and streams it into the asset store.
func (e *Executor) fetchAndUploadArtifact(ctx context.Context, dir string, plan *local.DockerRunPlan, t rebuild.Target, store rebuild.AssetStore) error {
	// The build ctx may be spent. Retrieval gets its own bounded context.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	artifactPath := path.Join(dir, "out", path.Base(plan.OutputPath))
	script := strings.Join([]string{
		"set -eu",
		fmt.Sprintf("[ -f %q ] || exit %d", artifactPath, exitNoArtifact),
		fmt.Sprintf(`[ "$(wc -c < %q)" -le %d ] || exit %d`, artifactPath, e.maxArtifactBytes, exitArtifactTooBig),
		fmt.Sprintf("base64 %q", artifactPath),
	}, "\n")
	op, err := Exec(fctx, e.stubs, schema.ScratchExecRequest{
		ScratchID:      e.scratchID,
		Cmd:            []string{"/bin/sh", "-c", script},
		TimeoutSeconds: int(utilityTimeout.Seconds()),
	}, e.pollInterval)
	if err != nil {
		return errors.Wrap(err, "fetching artifact")
	}
	if op.Error != nil {
		return errors.Wrap(op.Error, "artifact exec")
	}
	switch op.Result.ExitCode {
	case 0:
	case exitNoArtifact:
		return ErrNoArtifact
	case exitArtifactTooBig:
		return errors.Errorf("artifact exceeds retrieval cap (%d bytes)", e.maxArtifactBytes)
	default:
		return errors.Errorf("artifact retrieval failed with exit code %d", op.Result.ExitCode)
	}
	// An absent output object means base64 produced no bytes: an empty
	// artifact.
	rd, err := e.outputReader(fctx, op)
	if err != nil {
		return errors.Wrap(err, "resolving artifact output")
	}
	defer rd.Close()
	dec := base64.NewDecoder(base64.StdEncoding, newlineFilteringReader{r: rd})
	return errors.Wrap(e.uploadStream(fctx, store, rebuild.RebuildAsset.For(t), dec), "uploading artifact")
}

// outputReader opens the op's merged output object, yielding an empty reader
// when no output was ever synced.
func (e *Executor) outputReader(ctx context.Context, op *longrunning.Operation[schema.ScratchExecResult]) (io.ReadCloser, error) {
	obj, err := outputObject(e.gcsClient, op)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	rd, err := obj.NewReader(ctx)
	if errors.Is(err, gcs.ErrObjectNotExist) {
		return io.NopCloser(strings.NewReader("")), nil
	} else if err != nil {
		return nil, errors.Wrap(err, "opening output object")
	}
	return rd, nil
}

// uploadStream copies content into the asset store.
// TODO: Copy GCS-to-GCS uploads server-side.
func (e *Executor) uploadStream(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, r io.Reader) error {
	w, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "creating asset writer")
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return errors.Wrap(err, "writing asset")
	}
	return errors.Wrap(w.Close(), "finalizing asset")
}

// newlineFilteringReader strips CR/LF from a base64 stream: base64(1) wraps
// lines and encoding/base64 does not tolerate newlines.
type newlineFilteringReader struct {
	r io.Reader
}

func (f newlineFilteringReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	kept := 0
	for i := range n {
		if p[i] == '\n' || p[i] == '\r' {
			continue
		}
		p[kept] = p[i]
		kept++
	}
	// Report progress even when a chunk was all newlines, unless the
	// underlying reader is exhausted.
	if kept == 0 && n > 0 && err == nil {
		return f.Read(p)
	}
	return kept, err
}
