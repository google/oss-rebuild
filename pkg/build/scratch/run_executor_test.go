// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// runCreates scripts successful prepare and start ops plus one phase op
// per exit code, then the post-phase stop.
func runCreates(phaseExits ...int) []createFn {
	creates := []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("start", 0), nil),
	}
	for _, exit := range phaseExits {
		creates = append(creates, respond(completedOp("phase", exit), nil))
	}
	return append(creates, respond(completedOp("stop", 0), nil))
}

func testRunExecutor(t *testing.T, f *fakeStubs) *DockerRunExecutor {
	t.Helper()
	e, err := NewDockerRunExecutor(DockerRunExecutorConfig{ExecutorConfig: testExecutorConfig(f)})
	if err != nil {
		t.Fatalf("NewDockerRunExecutor: %v", err)
	}
	return e
}

func TestPhaseFailureSurfacesExitError(t *testing.T) {
	// Phases: setup, source succeed and deps exits 42. The build phase and
	// artifact fetch must not be dispatched.
	f := &fakeStubs{creates: runCreates(0, 0, 42)}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Code != 42 || exitErr.Phase != "deps" {
		t.Fatalf("Result.Error = %v, want ExitError{42, deps}", result.Error)
	}
	// The scripted ops carry no worker stamps, so the record holds a failure
	// marker alone and Validated refuses it.
	if result.Timings != nil {
		t.Errorf("Expected nil timings without worker stamps, got %+v", result.Timings)
	}
	if len(f.createReqs) != 6 {
		t.Fatalf("got %d exec creates, want 6 (prepare, start, 3 phases, stop)", len(f.createReqs))
	}
	prepare, start := f.createReqs[0], f.createReqs[1]
	// Prepare creates the output mount point and sweeps prior containers.
	prepareScript := prepare.Cmd[len(prepare.Cmd)-1]
	for _, want := range []string{"mkdir -p", "docker rm -f"} {
		if !strings.Contains(prepareScript, want) {
			t.Errorf("prepare script missing %q:\n%s", want, prepareScript)
		}
	}
	// Start launches the idle container with direct docker argv.
	if start.Cmd[0] != "docker" || start.Cmd[1] != "run" {
		t.Errorf("start cmd = %v, want docker run argv", start.Cmd)
	}
	for _, want := range []string{"--detach", "--rm", "rb-iter-1", "sleep", "infinity"} {
		if !slices.Contains(start.Cmd, want) {
			t.Errorf("start cmd missing %q: %v", want, start.Cmd)
		}
	}
	// The stop follows the failing phase, letting --rm reclaim the container.
	if stop, want := f.createReqs[5], []string{"docker", "stop", "-t", "0", "rb-iter-1"}; !slices.Equal(stop.Cmd, want) {
		t.Errorf("stop cmd = %v, want %v", stop.Cmd, want)
	}
	// Phase ops exec into the container with the script inline in the
	// argv, documenting on the persisted record exactly what ran.
	wantScript := []string{"apk", "git clone", "npm install"}
	for i, phase := range f.createReqs[2:5] {
		wantPrefix := []string{"docker", "exec", "rb-iter-1", "/bin/sh", "-c"}
		if len(phase.Cmd) != len(wantPrefix)+1 || !slices.Equal(phase.Cmd[:len(wantPrefix)], wantPrefix) {
			t.Errorf("phase %d cmd = %v, want %v plus script", i, phase.Cmd, wantPrefix)
			continue
		}
		script := phase.Cmd[len(phase.Cmd)-1]
		if !strings.HasPrefix(script, "set -eux\n") {
			t.Errorf("phase %d script missing strict-mode prelude:\n%s", i, script)
		}
		if !strings.Contains(script, wantScript[i]) {
			t.Errorf("phase %d script missing %q:\n%s", i, wantScript[i], script)
		}
		if phase.StdinB64 != "" {
			t.Errorf("phase %d carries stdin: %q", i, phase.StdinB64)
		}
	}
}

func TestUtilityStepFailures(t *testing.T) {
	for _, tt := range []struct {
		name    string
		creates []createFn
		want    string
	}{
		{name: "PrepareDispatchError", creates: []createFn{respond(nil, errors.New("scratch \"s1\" not found"))}, want: "preparing build"},
		{name: "StartNonzeroExit", creates: []createFn{respond(completedOp("prepare", 0), nil), respond(completedOp("start", 125), nil)}, want: "starting build container"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStubs{creates: tt.creates}
			result := awaitBuild(t, testRunExecutor(t, f), testOptions())
			if result.Error == nil || !strings.Contains(result.Error.Error(), tt.want) {
				t.Fatalf("Result.Error = %v, want %q", result.Error, tt.want)
			}
		})
	}
}

func TestTimeoutBudget(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout time.Duration
		want    int
	}{
		{name: "UnsetUsesDefault", timeout: 0, want: int(defaultBuildTimeout.Seconds())},
		{name: "SubsecondClampsToOne", timeout: 500 * time.Millisecond, want: 1},
		{name: "ExplicitPassesThrough", timeout: 2 * time.Minute, want: 120},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStubs{creates: runCreates(1)}
			opts := testOptions()
			opts.Timeout = tt.timeout
			awaitBuild(t, testRunExecutor(t, f), opts)
			// The utility ops carry the utility bound. The first phase op
			// gets the full build budget.
			for _, i := range []int{0, 1, 3} {
				if got := f.createReqs[i].TimeoutSeconds; got != int(utilityTimeout.Seconds()) {
					t.Errorf("op %d TimeoutSeconds = %d, want utility %d", i, got, int(utilityTimeout.Seconds()))
				}
			}
			if got := f.createReqs[2].TimeoutSeconds; got != tt.want {
				t.Errorf("phase TimeoutSeconds = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMissingArtifactFailsBuild(t *testing.T) {
	f := &fakeStubs{
		creates: append(runCreates(0, 0, 0, 0), respond(completedOp("fetch", exitNoArtifact), nil)),
	}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	if !errors.Is(result.Error, ErrNoArtifact) {
		t.Fatalf("Result.Error = %v, want ErrNoArtifact", result.Error)
	}
}

func TestOversizeArtifactFailsBuild(t *testing.T) {
	f := &fakeStubs{
		creates: append(runCreates(0, 0, 0, 0), respond(completedOp("fetch", exitArtifactTooBig), nil)),
	}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	if result.Error == nil || !strings.Contains(result.Error.Error(), "retrieval cap") {
		t.Fatalf("Result.Error = %v, want retrieval cap error", result.Error)
	}
}

func TestSuccessUploadsAssets(t *testing.T) {
	f := &fakeStubs{
		// No OutURI on the fetch op: an empty artifact.
		creates: append(runCreates(0, 0, 0, 0), respond(completedOp("fetch", 0), nil)),
	}
	opts := testOptions()
	result := awaitBuild(t, testRunExecutor(t, f), opts)
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	// Both assets exist in the store (empty, since the fake ops carry no output).
	for _, asset := range []rebuild.Asset{rebuild.RebuildAsset.For(testTarget), rebuild.DebugLogsAsset.For(testTarget)} {
		r, err := opts.Resources.AssetStore.Reader(context.Background(), asset)
		if err != nil {
			t.Errorf("missing asset %v: %v", asset, err)
			continue
		}
		r.Close()
	}
	// The fetch script guards existence and size before encoding.
	fetch := f.createReqs[7]
	script := fetch.Cmd[len(fetch.Cmd)-1]
	for _, want := range []string{"base64", "wc -c"} {
		if !strings.Contains(script, want) {
			t.Errorf("fetch script missing %q:\n%s", want, script)
		}
	}
}

func TestTimingsFromWorkerStamps(t *testing.T) {
	// Each phase op carries worker-clock stamps; the spans must be their
	// differences, not client-observed wall time.
	base := time.Unix(1700000000, 0)
	timedOp := func(id string, i int) *longrunning.Operation[schema.ScratchExecResult] {
		op := completedOp(id, 0)
		op.Result.StartedAt = base.Add(time.Duration(i) * time.Minute)
		op.Result.FinishedAt = op.Result.StartedAt.Add(time.Duration(i+1) * time.Second)
		return op
	}
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("start", 0), nil),
		respond(timedOp("setup", 0), nil),
		respond(timedOp("source", 1), nil),
		respond(timedOp("deps", 2), nil),
		respond(timedOp("build", 3), nil),
		respond(completedOp("stop", 0), nil),
		respond(completedOp("fetch", 0), nil),
	}}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	want := &rebuild.BuildTimings{Setup: durPtr(time.Second), Source: durPtr(2 * time.Second), Deps: durPtr(3 * time.Second), Build: durPtr(4 * time.Second)}
	if diff := cmp.Diff(want, result.Timings); diff != "" {
		t.Errorf("Timings mismatch (-want +got):\n%s", diff)
	}
}

func TestTimingsAbsentWithoutWorkerStamps(t *testing.T) {
	// completedOp carries no StartedAt/FinishedAt: blind finalization. The
	// build succeeds but every phase is unmeasured, so no record is emitted.
	f := &fakeStubs{creates: append(runCreates(0, 0, 0, 0), respond(completedOp("fetch", 0), nil))}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	if result.Timings != nil {
		t.Errorf("Expected nil timings without worker stamps, got %+v", result.Timings)
	}
}

func TestTimingsPartialOnPhaseFailure(t *testing.T) {
	// A nonzero-exit phase op still carries worker stamps, so the failing
	// span is measured; the build phase is never dispatched and stays nil.
	base := time.Unix(1700000000, 0)
	stamped := func(id string, exit, i int) *longrunning.Operation[schema.ScratchExecResult] {
		op := completedOp(id, exit)
		op.Result.StartedAt = base.Add(time.Duration(i) * time.Minute)
		op.Result.FinishedAt = op.Result.StartedAt.Add(time.Duration(i+1) * time.Second)
		return op
	}
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("start", 0), nil),
		respond(stamped("setup", 0, 0), nil),
		respond(stamped("source", 0, 1), nil),
		respond(stamped("deps", 42, 2), nil),
		respond(completedOp("stop", 0), nil),
	}}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Phase != "deps" {
		t.Fatalf("Result.Error = %v, want ExitError in deps", result.Error)
	}
	want := &rebuild.BuildTimings{Setup: durPtr(time.Second), Source: durPtr(2 * time.Second), Deps: durPtr(3 * time.Second), FailedIn: rebuild.PhaseDeps}
	if diff := cmp.Diff(want, result.Timings); diff != "" {
		t.Errorf("Timings mismatch (-want +got):\n%s", diff)
	}
}

func TestTimingsPartialOnUnstampedFailure(t *testing.T) {
	// A blind-finalized failing op has no worker stamps: the measured prefix
	// survives with the marker while the failing phase stays unmeasured.
	base := time.Unix(1700000000, 0)
	stamped := func(id string, i int) *longrunning.Operation[schema.ScratchExecResult] {
		op := completedOp(id, 0)
		op.Result.StartedAt = base.Add(time.Duration(i) * time.Minute)
		op.Result.FinishedAt = op.Result.StartedAt.Add(time.Duration(i+1) * time.Second)
		return op
	}
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("start", 0), nil),
		respond(stamped("setup", 0), nil),
		respond(stamped("source", 1), nil),
		respond(completedOp("deps", 42), nil),
		respond(completedOp("stop", 0), nil),
	}}
	result := awaitBuild(t, testRunExecutor(t, f), testOptions())
	if result.Error == nil {
		t.Fatal("Expected error, got success")
	}
	want := &rebuild.BuildTimings{Setup: durPtr(time.Second), Source: durPtr(2 * time.Second), FailedIn: rebuild.PhaseDeps}
	if diff := cmp.Diff(want, result.Timings); diff != "" {
		t.Errorf("Timings mismatch (-want +got):\n%s", diff)
	}
}

func TestAuthHeaderViaEnvOnly(t *testing.T) {
	f := &fakeStubs{creates: runCreates(1)}
	cfg := testExecutorConfig(f)
	cfg.AuthHeader = func(context.Context) (string, error) {
		return "Authorization: Bearer sekrit", nil
	}
	e, err := NewDockerRunExecutor(DockerRunExecutorConfig{ExecutorConfig: cfg})
	if err != nil {
		t.Fatalf("NewDockerRunExecutor: %v", err)
	}
	opts := testOptions()
	opts.Resources.ToolAuthRequired = []string{"gs://test-bootstrap"}
	awaitBuild(t, e, opts)
	// The container start carries the header via op env and the value-less
	// -e form in argv. The phase ops inherit it from the container env and
	// carry nothing themselves.
	start := f.createReqs[1]
	if start.Env["AUTH_HEADER"] != "Authorization: Bearer sekrit" {
		t.Errorf("start Env = %v, want AUTH_HEADER set", start.Env)
	}
	for i, arg := range start.Cmd {
		if strings.Contains(arg, "sekrit") {
			t.Errorf("start argv[%d] leaks the auth value: %q", i, arg)
		}
	}
	if !slices.Contains(start.Cmd, "AUTH_HEADER") {
		t.Errorf("start argv missing value-less -e AUTH_HEADER: %v", start.Cmd)
	}
	phase := f.createReqs[2]
	if len(phase.Env) != 0 {
		t.Errorf("phase Env = %v, want empty", phase.Env)
	}
	// The inline phase script may reference AUTH_HEADER by name only.
	for i, arg := range phase.Cmd {
		if strings.Contains(arg, "sekrit") {
			t.Errorf("phase argv[%d] leaks the auth value: %q", i, arg)
		}
	}
}

func TestRetainContainerSkipsRm(t *testing.T) {
	f := &fakeStubs{creates: runCreates(1)}
	cfg := testExecutorConfig(f)
	cfg.RetainContainer = true
	e, err := NewDockerRunExecutor(DockerRunExecutorConfig{ExecutorConfig: cfg})
	if err != nil {
		t.Fatalf("NewDockerRunExecutor: %v", err)
	}
	awaitBuild(t, e, testOptions())
	if start := f.createReqs[1]; slices.Contains(start.Cmd, "--rm") {
		t.Errorf("start cmd carries --rm, want retained container: %v", start.Cmd)
	}
}
