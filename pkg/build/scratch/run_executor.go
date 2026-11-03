// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
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

// SeedRepoPath is the bare repository on the VM that a session seeds from the
// git cache, so builds clone its repository without refetching it.
func SeedRepoPath(workDir string) string { return path.Join(workDir, "seed", "repo.git") }

// ContainerSeedRepoPath is the seed repository's path inside a build container.
const ContainerSeedRepoPath = "/seed/repo.git"

var _ build.Executor = (*DockerRunExecutor)(nil)

// ArtifactPath is where a docker run build leaves its artifact on the VM.
func ArtifactPath(workDir, buildID string) string {
	return path.Join(BuildDir(workDir, buildID), "out", path.Base(local.DockerRunOutputPath))
}

// SourcePath is where a docker run build's source tree lives on the VM: the
// host side of the container's /src bind mount. It outlives the container,
// until the next build's prepare sweep.
func SourcePath(workDir, buildID string) string {
	return path.Join(BuildDir(workDir, buildID), "src")
}

// prepareSourceMount removes every earlier build's source tree under workDir
// and creates this build's. Like the containers, the trees are retained until
// the next build, and a checkout with its dependency trees is the bulk of a
// build's footprint.
func prepareSourceMount(workDir, buildID string) string {
	return fmt.Sprintf("rm -rf %q/*/src && mkdir -p %q", path.Join(workDir, buildsSubdir), SourcePath(workDir, buildID))
}

// seedCloneScript clones repo into dir without a checkout, using the VM's git.
// It copies the objects the seed holds and fetches only the rest, and the clone
// keeps no link to the seed. Without a seed it clones in full.
func seedCloneScript(seed, repo, dir string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return fmt.Sprintf("git clone --quiet --no-checkout --reference-if-able %s --dissociate -- %s %s", q(seed), q(repo), q(dir))
}

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
// directory is always retained for post-mortem inspection. Timings may be
// partial and accompany a build error or a failed artifact retrieval.
func (e *DockerRunExecutor) runBuild(ctx context.Context, handle *scratchHandle, plan *local.DockerRunPlan, t rebuild.Target, opts build.Options, timeout time.Duration) (*rebuild.BuildTimings, error) {
	dir := BuildDir(e.workDir, handle.id)
	container := "rb-" + handle.id
	prep := []string{prepareSourceMount(e.workDir, handle.id)}
	if e.singleEnv {
		prep = append(prep, fmt.Sprintf("mkdir -p %q %q", ToolsDir(e.workDir), UpstreamDir(e.workDir)))
	}
	if plan.RepoURL != "" {
		// The mount needs a source even before the session seeds it.
		prep = append(prep, fmt.Sprintf("mkdir -p %q", SeedRepoPath(e.workDir)))
	}
	if err := e.prepareBuild(ctx, dir, "build.sh", []byte(plan.CombinedScript()), prep...); err != nil {
		return nil, err
	}
	argOpts := local.RunArgsOpts{
		ContainerName:   container,
		OutputMountSrc:  dir + "/out",
		SourceMountSrc:  SourcePath(e.workDir, handle.id),
		Remove:          !e.retainContainer,
		AllowPrivileged: e.allowPrivileged,
	}
	if e.singleEnv {
		argOpts.ReadOnlyMounts = singleEnvMounts(e.workDir)
	}
	if plan.RepoURL != "" {
		argOpts.ReadOnlyMounts = append(argOpts.ReadOnlyMounts, local.Mount{HostPath: SeedRepoPath(e.workDir), ContainerPath: ContainerSeedRepoPath})
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
	if err := e.utilityExec(ctx, append([]string{"docker"}, local.ComposeDockerStartArgs(plan, argOpts)...), env, schema.ScratchLabelStart, "starting build container"); err != nil {
		return nil, err
	}
	// The phases share one wall-clock budget, decremented by phaseExec.
	handle.updateStatus(build.BuildStateRunning)
	var phaseOps []*longrunning.Operation[schema.ScratchExecResult]
	var buildErr error
	elapsed := rebuild.BuildTimings{}
	slots := map[rebuild.BuildPhase]**time.Duration{rebuild.PhaseSetup: &elapsed.Setup, rebuild.PhaseSource: &elapsed.Source, rebuild.PhaseDeps: &elapsed.Deps, rebuild.PhaseBuild: &elapsed.Build}
	remaining := timeout
	type step struct {
		phase rebuild.BuildPhase
		cmd   []string
	}
	var steps []step
	if plan.RepoURL != "" {
		// The plan leaves the clone to the executor, which runs it on the VM
		// as the first step of sourcing.
		steps = append(steps, step{rebuild.PhaseSource, []string{"/bin/sh", "-c", seedCloneScript(SeedRepoPath(e.workDir), plan.RepoURL, SourcePath(e.workDir, handle.id))}})
	}
	for _, ph := range plan.Phases() {
		steps = append(steps, step{ph.Name, append([]string{"docker"}, local.ComposeDockerExecArgs(container, ph)...)})
	}
	for _, st := range steps {
		op, err := e.phaseExec(ctx, handle, schema.ScratchExecRequest{Cmd: st.cmd, Label: schema.PhaseLabel(st.phase)}, &remaining)
		if err == nil {
			phaseOps = append(phaseOps, op)
		}
		// Spans come from the worker's clock: StartedAt/FinishedAt bound the
		// command execution on the VM, excluding exec dispatch and poll lag.
		// They survive a nonzero exit and a worker-synced timeout, so the
		// failing phase is usually measured. Blind finalization leaves them
		// zero and the phase unmeasured.
		if op != nil && op.Result != nil && !op.Result.StartedAt.IsZero() && !op.Result.FinishedAt.IsZero() {
			d := op.Result.FinishedAt.Sub(op.Result.StartedAt)
			if prev := *slots[st.phase]; prev != nil {
				d += *prev // the clone and the source phase share a slot
			}
			*slots[st.phase] = &d
		}
		if buildErr = phaseOutcome(string(st.phase), op, err); buildErr != nil {
			elapsed.FailedIn = st.phase
			// Best effort: name the failing command in the agent-facing error.
			var ee *ExitError
			if errors.As(buildErr, &ee) {
				ee.Command = e.failingCommand(ctx, op)
			}
			break
		}
	}
	// A present zero Deps means the plan had no deps phase, stamped only once
	// the build provably progressed past the deps slot.
	if plan.Deps == "" && (elapsed.FailedIn == "" || elapsed.FailedIn == rebuild.PhaseBuild) {
		elapsed.Deps = new(time.Duration)
	}
	e.stopContainer(ctx, container)
	e.uploadDebugLogs(ctx, handle.id, phaseOps, t, opts.Resources.AssetStore)
	// Undispatched phases stay unmeasured. Validated owns the partial-record
	// invariants.
	timings := timing.Validated(elapsed)
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

// stopContainer halts the build container's idle init once the phases are
// done, letting --rm reclaim it. Best effort under a fresh context so it
// runs on cancellation. The next build's prepare sweep covers missed stops.
func (e *DockerRunExecutor) stopContainer(ctx context.Context, container string) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityTimeout)
	defer cancel()
	if err := e.utilityExec(sctx, []string{"docker", "stop", "-t", "0", container}, nil, schema.ScratchLabelStop, "stopping build container"); err != nil {
		log.Printf("build container %s: %v", container, err)
	}
}
