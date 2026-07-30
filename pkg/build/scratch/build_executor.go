// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/build/timing"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// DockerBuildExecutorConfig configures a scratch DockerBuildExecutor.
type DockerBuildExecutorConfig struct {
	ExecutorConfig
	Planner     build.Planner[*local.DockerBuildPlan] // Defaults to local.NewDockerBuildPlanner()
	RetainImage bool                                  // If true, don't remove the built image after the build completes
}

// DockerBuildExecutor implements build.Executor on a scratch VM by building
// an image from the plan's Dockerfile and running it, mirroring
// local.DockerBuildExecutor's docker buildx build + docker run composition.
// See executor for the shared lifecycle and cancellation semantics.
type DockerBuildExecutor struct {
	*executor
	planner     build.Planner[*local.DockerBuildPlan]
	retainImage bool
}

var _ build.Executor = (*DockerBuildExecutor)(nil)

// NewDockerBuildExecutor creates a scratch docker build executor from config.
func NewDockerBuildExecutor(config DockerBuildExecutorConfig) (*DockerBuildExecutor, error) {
	core, err := newExecutor(config.ExecutorConfig)
	if err != nil {
		return nil, err
	}
	planner := config.Planner
	if planner == nil {
		planner = local.NewDockerBuildPlanner()
	}
	return &DockerBuildExecutor{executor: core, planner: planner, retainImage: config.RetainImage}, nil
}

// Start implements build.Executor.
func (e *DockerBuildExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
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
	// A context directory names a client-local path with no counterpart on
	// the VM.
	if plan.ContextDir != "" {
		return nil, errors.New("build contexts are not supported by the scratch executor")
	}
	return e.startBuild(opts, func(ctx context.Context, handle *scratchHandle, timeout time.Duration) (*rebuild.BuildTimings, error) {
		return e.runBuild(ctx, handle, plan, input.Target, opts, timeout)
	})
}

// runBuild drives one image-build-then-run build on the scratch VM. The
// image and container are removed after observation unless retained. The
// staging directory is always retained for post-mortem inspection.
func (e *DockerBuildExecutor) runBuild(ctx context.Context, handle *scratchHandle, plan *local.DockerBuildPlan, t rebuild.Target, opts build.Options, timeout time.Duration) (*rebuild.BuildTimings, error) {
	dir := path.Join(e.workDir, buildsSubdir, handle.id)
	container := "rb-" + handle.id
	// Image references must be lowercase, unlike container names.
	imageTag := "rb-" + strings.ToLower(handle.id)
	// The planner declares layers only under Options.RecordTimings, so the
	// declaration is the observation gate. The container is retained when
	// observed so its state clocks survive for inspection.
	recordTimings := plan.Layers.Appended > 0
	autoRemove := !e.retainContainer && !recordTimings
	if err := e.prepareBuild(ctx, dir, "docker images -q --filter 'reference=rb-*' | xargs -r docker rmi -f"); err != nil {
		return nil, err
	}
	if plan.Privileged && !e.allowPrivileged {
		log.Println("Warning: plan requested privileged execution but this executor does not allow privileged builds.")
	}
	// The image build and container run share one wall-clock budget,
	// decremented by phaseExec. The Dockerfile arrives via the transient
	// exec stdin channel, which the persisted exec record omits, so the
	// DockerfileAsset upload below is its durable record.
	buildCmd := []string{"docker", "buildx", "build", "-t", imageTag}
	var env map[string]string
	if plan.RequiresAuth {
		if e.authHeader == nil {
			return nil, errors.New("build plan requires auth but no auth header source is configured")
		}
		header, err := e.authHeader(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "generating auth header")
		}
		// The value reaches the secret through the transient request env,
		// staying out of the persisted argv.
		env = map[string]string{"AUTH_HEADER": header}
		buildCmd = append(buildCmd, "--secret", "id=auth_header,env=AUTH_HEADER")
	}
	buildCmd = append(buildCmd, "-")
	handle.updateStatus(build.BuildStateRunning)
	remaining := timeout
	var phaseOps []*longrunning.Operation[schema.ScratchExecResult]
	var timings *rebuild.BuildTimings
	op, err := e.phaseExec(ctx, handle, schema.ScratchExecRequest{
		Cmd:      buildCmd,
		Env:      env,
		StdinB64: base64.StdEncoding.EncodeToString([]byte(plan.Dockerfile)),
	}, &remaining)
	if err == nil {
		phaseOps = append(phaseOps, op)
	}
	buildErr := phaseOutcome("image build", op, err)
	if buildErr == nil {
		// Anchors setup at the buildx process start on the VM, sharing a
		// host clock with the layer stamps it is compared against.
		buildStart := op.Result.StartedAt
		runCmd := []string{"docker", "run", "--name", container}
		if autoRemove {
			runCmd = append(runCmd, "--rm")
		}
		runCmd = append(runCmd, "-v", fmt.Sprintf("%s:%s", dir+"/out", path.Dir(plan.OutputPath)))
		if plan.Privileged && e.allowPrivileged {
			runCmd = append(runCmd, "--privileged", "-v", "/var/run/docker.sock:/var/run/docker.sock")
		}
		// Disable core dumps
		runCmd = append(runCmd, "--ulimit", "core=0", imageTag)
		op, err = e.phaseExec(ctx, handle, schema.ScratchExecRequest{Cmd: runCmd}, &remaining)
		if err == nil {
			phaseOps = append(phaseOps, op)
		}
		buildErr = phaseOutcome("container run", op, err)
		// Extract timings before cleanup discards the image history and
		// container state clocks.
		if recordTimings {
			timings = e.extractTimings(ctx, container, imageTag, plan, buildStart, buildErr != nil)
		}
		e.cleanupBuild(ctx, container, imageTag, autoRemove)
	}
	e.uploadDebugLogs(ctx, handle.id, phaseOps, t, opts.Resources.AssetStore)
	e.uploadDockerfile(ctx, handle.id, plan, t, opts.Resources.AssetStore)
	if buildErr != nil {
		return timings, buildErr
	}
	// Retrieve and upload the artifact. Unlike the local executor, a
	// missing artifact or failed upload fails the build.
	if opts.Resources.AssetStore != nil {
		return timings, e.fetchAndUploadArtifact(ctx, dir, plan.OutputPath, t, opts.Resources.AssetStore)
	}
	return timings, nil
}

// extractTimings assembles phase timings from the image history and the
// retained container's state clocks, read off the VM's docker daemon.
// buildStart is zero under blind finalization: the record is then
// incomplete and no timings are reported. A failed run yields a marked
// record: the image phases completed (the image exists), and an unusable
// container span (a still-running or unclocked container) leaves Build
// unmeasured without discarding them.
// NOTE: Extraction failures are logged, never surfaced as build errors.
func (e *DockerBuildExecutor) extractTimings(ctx context.Context, container, imageTag string, plan *local.DockerBuildPlan, buildStart time.Time, runFailed bool) *rebuild.BuildTimings {
	if buildStart.IsZero() {
		log.Printf("Build %s timing skipped: no worker clock for the image build", container)
		return nil
	}
	hist, err := e.utilityExecOutput(ctx, []string{"docker", "history", "--human=false", "--format", "{{json .}}", imageTag}, "timing history")
	if err != nil {
		log.Printf("Build %s: %v", container, err)
		return nil
	}
	layers, err := timing.ParseHistory(hist, plan.Layers)
	if err != nil {
		log.Printf("Build %s timing history unparseable: %v", container, err)
		return nil
	}
	setup, source, deps, err := layers.Phases(buildStart)
	if err != nil {
		log.Printf("Build %s timing extraction refused: %v", container, err)
		return nil
	}
	in := rebuild.BuildTimings{Setup: &setup, Source: &source, Deps: &deps}
	if runFailed {
		in.FailedIn = rebuild.PhaseBuild
	}
	span, err := e.utilityExecOutput(ctx, []string{"docker", "inspect", container, "-f", "{{.State.StartedAt}} {{.State.FinishedAt}}"}, "timing inspect")
	if err != nil {
		log.Printf("Build %s: %v", container, err)
	} else if buildDur, err := timing.ContainerSpan(span); err != nil {
		log.Printf("Build %s timing inspect unparseable: %v", container, err)
	} else if buildDur <= 0 {
		log.Printf("Build %s container span non-positive: %v", container, buildDur)
	} else {
		in.Build = &buildDur
	}
	return timing.Validated(in)
}

// cleanupBuild removes the build's container and image unless retention or
// --rm already covers them. Best effort under a fresh context so it runs on
// cancellation. The next build's prepare sweep covers missed removals.
func (e *DockerBuildExecutor) cleanupBuild(ctx context.Context, container, imageTag string, autoRemove bool) {
	var script []string
	if !autoRemove && !e.retainContainer {
		// Tolerate a container the failed run never created.
		script = append(script, fmt.Sprintf("docker rm -f %q || true", container))
	}
	if !e.retainImage {
		script = append(script, fmt.Sprintf("docker rmi %q", imageTag))
	}
	if len(script) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	if err := e.utilityExec(cctx, []string{"/bin/sh", "-c", strings.Join(script, "\n")}, nil, "cleaning up build"); err != nil {
		log.Printf("build %s: %v", container, err)
	}
}

// uploadDockerfile records the plan's Dockerfile in the asset store since
// the exec record does not persist the stdin channel that delivered it.
// Upload failures never fail the build.
func (e *DockerBuildExecutor) uploadDockerfile(ctx context.Context, buildID string, plan *local.DockerBuildPlan, t rebuild.Target, store rebuild.AssetStore) {
	if store == nil {
		return
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	if err := e.uploadStream(uctx, store, rebuild.DockerfileAsset.For(t), strings.NewReader(plan.Dockerfile)); err != nil {
		log.Printf("build %s: uploading Dockerfile: %v", buildID, err)
	}
}
