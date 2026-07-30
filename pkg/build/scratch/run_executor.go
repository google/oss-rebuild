// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"log"
	"path"
	"time"

	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/build/timing"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// DockerRunExecutorConfig configures a scratch DockerRunExecutor.
type DockerRunExecutorConfig struct {
	ExecutorConfig
	Planner build.Planner[*local.DockerRunPlan] // Defaults to local.NewDockerRunPlanner()
}

// DockerRunExecutor implements build.Executor on a scratch VM by starting
// the build container idle and driving the plan's phase scripts through it
// sequentially, mirroring local.DockerRunExecutor's docker start/exec
// composition. See executor for the shared lifecycle and cancellation
// semantics.
type DockerRunExecutor struct {
	*executor
	planner build.Planner[*local.DockerRunPlan]
}

var _ build.Executor = (*DockerRunExecutor)(nil)

// NewDockerRunExecutor creates a scratch docker run executor from config.
func NewDockerRunExecutor(config DockerRunExecutorConfig) (*DockerRunExecutor, error) {
	core, err := newExecutor(config.ExecutorConfig)
	if err != nil {
		return nil, err
	}
	planner := config.Planner
	if planner == nil {
		planner = local.NewDockerRunPlanner()
	}
	return &DockerRunExecutor{executor: core, planner: planner}, nil
}

// Start implements build.Executor.
func (e *DockerRunExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
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
	return e.startBuild(opts, func(ctx context.Context, handle *scratchHandle, timeout time.Duration) (*rebuild.BuildTimings, error) {
		return e.runBuild(ctx, handle, plan, input.Target, opts, timeout)
	})
}

// runBuild drives one build on the scratch VM. The container is stopped and,
// unless RetainContainer is set, removed after the phases. The staging
// directory is always retained for post-mortem inspection. Timings are
// non-nil whenever every phase completed measured, even if artifact
// retrieval subsequently fails.
func (e *DockerRunExecutor) runBuild(ctx context.Context, handle *scratchHandle, plan *local.DockerRunPlan, t rebuild.Target, opts build.Options, timeout time.Duration) (*rebuild.BuildTimings, error) {
	dir := path.Join(e.workDir, buildsSubdir, handle.id)
	container := "rb-" + handle.id
	if err := e.prepareBuild(ctx, dir); err != nil {
		return nil, err
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
			return nil, errors.New("build plan requires auth but no auth header source is configured")
		}
		header, err := e.authHeader(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "generating auth header")
		}
		env = map[string]string{"AUTH_HEADER": header}
		argOpts.AuthMode = local.AuthEnvPassthrough
	}
	if err := e.utilityExec(ctx, append([]string{"docker"}, local.ComposeDockerStartArgs(plan, argOpts)...), env, "starting build container"); err != nil {
		return nil, err
	}
	// The phases share one wall-clock budget, decremented by phaseExec.
	handle.updateStatus(build.BuildStateRunning)
	var phaseOps []*longrunning.Operation[schema.ScratchExecResult]
	var buildErr error
	in := rebuild.BuildTimings{}
	spans := map[string]*time.Duration{"setup": &in.Setup, "source": &in.Source, "deps": &in.Deps, "build": &in.Build}
	measured := true
	remaining := timeout
	for _, ph := range plan.Phases() {
		op, err := e.phaseExec(ctx, handle, schema.ScratchExecRequest{
			Cmd: append([]string{"docker"}, local.ComposeDockerExecArgs(container, ph)...),
		}, &remaining)
		if err == nil {
			phaseOps = append(phaseOps, op)
		}
		if buildErr = phaseOutcome(ph.Name, op, err); buildErr != nil {
			break
		}
		// Spans come from the worker's clock: StartedAt/FinishedAt bound the
		// command execution on the VM, excluding exec dispatch and poll lag.
		// StartedAt is zero under blind finalization; the record is then
		// incomplete and no timings are reported.
		if r := op.Result; r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
			measured = false
		} else {
			*spans[ph.Name] = r.FinishedAt.Sub(r.StartedAt)
		}
	}
	e.stopContainer(ctx, container)
	e.uploadDebugLogs(ctx, handle.id, phaseOps, t, opts.Resources.AssetStore)
	if buildErr != nil {
		return nil, buildErr
	}
	// A failed build leaves later phases unmeasured. Validated owns the
	// all-or-nothing invariants.
	var timings *rebuild.BuildTimings
	if measured {
		timings = timing.Validated(in)
	}
	// Retrieve and upload the artifact. Unlike the local executor, a
	// missing artifact or failed upload fails the build.
	if opts.Resources.AssetStore != nil {
		return timings, e.fetchAndUploadArtifact(ctx, dir, plan.OutputPath, t, opts.Resources.AssetStore)
	}
	return timings, nil
}

// stopContainer halts the build container's idle init once the phases are
// done, letting --rm reclaim it. Best effort under a fresh context so it
// runs on cancellation. The next build's prepare sweep covers missed stops.
func (e *DockerRunExecutor) stopContainer(ctx context.Context, container string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	if err := e.utilityExec(sctx, []string{"docker", "stop", "-t", "0", container}, nil, "stopping build container"); err != nil {
		log.Printf("build container %s: %v", container, err)
	}
}
