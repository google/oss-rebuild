// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"encoding/base64"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
)

// testGCSClient satisfies the executor's required dependency. The fake ops
// carry no OutURI so it is never actually called.
var testGCSClient = must(gcs.NewClient(context.Background(), option.WithoutAuthentication()))

var testTarget = rebuild.Target{
	Ecosystem: rebuild.NPM,
	Package:   "lodash",
	Version:   "4.17.21",
	Artifact:  "lodash-4.17.21.tgz",
}

func testInput() rebuild.Input {
	return rebuild.Input{
		Target: testTarget,
		Strategy: &rebuild.ManualStrategy{
			Location: rebuild.Location{
				Repo: "https://github.com/lodash/lodash",
				Ref:  "aa1d7d870d9cf84842ee23ff485fd24abf0ed3d1",
				Dir:  ".",
			},
			Deps:       "npm install",
			Build:      "npm pack",
			OutputPath: "lodash-4.17.21.tgz",
		},
	}
}

func testOptions() build.Options {
	return build.Options{
		BuildID:     "iter-1",
		UseTimewarp: true,
		Resources: build.Resources{
			AssetStore:      rebuild.NewFilesystemAssetStore(memfs.New()),
			ToolURLs:        map[build.ToolType]string{build.TimewarpTool: "gs://test-bootstrap/timewarp"},
			BaseImageConfig: build.DefaultBaseImageConfig(),
		},
	}
}

func completedOp(id string, exitCode int) *longrunning.Operation[schema.ScratchExecResult] {
	return &longrunning.Operation[schema.ScratchExecResult]{
		ID:     id,
		Done:   true,
		Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: exitCode},
	}
}

func failedOp(id string, code codes.Code, msg string) *longrunning.Operation[schema.ScratchExecResult] {
	return &longrunning.Operation[schema.ScratchExecResult]{
		ID:     id,
		Done:   true,
		Error:  &longrunning.OperationError{Code: int(code), Message: msg},
		Result: &schema.ScratchExecResult{ScratchID: "s1"},
	}
}

// createFn is a scripted ExecCreate response.
type createFn = func(schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error)

// fakeStubs scripts exec create/get responses. Because executeBuild runs on
// a separate goroutine, unexpected calls are reported as errors rather than
// t.Fatal.
type fakeStubs struct {
	// creates is consumed one element per ExecCreate call.
	creates []createFn
	// gets is keyed by op ID and consumed one element per ExecGet call.
	gets map[string][]*longrunning.Operation[schema.ScratchExecResult]

	createReqs []schema.ScratchExecRequest
}

func (f *fakeStubs) stubs() Stubs {
	return Stubs{
		ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			f.createReqs = append(f.createReqs, req)
			if len(f.creates) == 0 {
				return nil, errors.Errorf("unexpected ExecCreate call: %v", req.Cmd)
			}
			next := f.creates[0]
			f.creates = f.creates[1:]
			return next(req)
		},
		ExecGet: func(_ context.Context, req schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			ops := f.gets[req.ID]
			if len(ops) == 0 {
				return nil, errors.Errorf("unexpected ExecGet call for op %q", req.ID)
			}
			op := ops[0]
			f.gets[req.ID] = ops[1:]
			return op, nil
		},
	}
}

// respond returns a scripted create response that ignores the request.
func respond(op *longrunning.Operation[schema.ScratchExecResult], err error) createFn {
	return func(schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
		return op, err
	}
}

// buildCreates scripts successful prepare and start ops plus one phase op
// per exit code, then the post-phase stop.
func buildCreates(phaseExits ...int) []createFn {
	creates := []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("start", 0), nil),
	}
	for _, exit := range phaseExits {
		creates = append(creates, respond(completedOp("phase", exit), nil))
	}
	return append(creates, respond(completedOp("stop", 0), nil))
}

func testExecutor(t *testing.T, f *fakeStubs) *Executor {
	t.Helper()
	e, err := NewExecutor(ExecutorConfig{
		ScratchID:    "s1",
		Stubs:        f.stubs(),
		GCSClient:    testGCSClient,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return e
}

func awaitBuild(t *testing.T, e *Executor, opts build.Options) build.Result {
	t.Helper()
	h, err := e.Start(context.Background(), testInput(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return result
}

func TestPhaseFailureSurfacesExitError(t *testing.T) {
	// Phases: setup, source succeed and deps exits 42. The build phase and
	// artifact fetch must not be dispatched.
	f := &fakeStubs{creates: buildCreates(0, 0, 42)}
	result := awaitBuild(t, testExecutor(t, f), testOptions())
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Code != 42 || exitErr.Phase != "deps" {
		t.Fatalf("Result.Error = %v, want ExitError{42, deps}", result.Error)
	}
	if result.Timings != nil {
		t.Errorf("Expected nil timings on failure, got %+v", result.Timings)
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

func TestPhaseOutcome(t *testing.T) {
	isInfra := func(err error) bool {
		var exitErr *ExitError
		return err != nil && !errors.As(err, &exitErr) && !errors.Is(err, context.DeadlineExceeded)
	}
	for _, tt := range []struct {
		name  string
		op    *longrunning.Operation[schema.ScratchExecResult]
		err   error
		check func(error) bool
	}{
		{name: "Success", op: completedOp("p", 0), check: func(err error) bool { return err == nil }},
		{name: "DispatchErrorPropagates", err: context.Canceled, check: func(err error) bool { return errors.Is(err, context.Canceled) }},
		{name: "TimeoutMapsToDeadlineExceeded", op: failedOp("p", codes.DeadlineExceeded, "command exceeded TimeoutSeconds"), check: func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }},
		{name: "LostIsInfraError", op: failedOp("p", codes.Unavailable, "worker dispatch failed"), check: isInfra},
		{name: "NonzeroExitIsExitError", op: completedOp("p", 42), check: func(err error) bool {
			var exitErr *ExitError
			return errors.As(err, &exitErr) && exitErr.Code == 42 && exitErr.Phase == "deps"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := phaseOutcome("deps", tt.op, tt.err); !tt.check(err) {
				t.Errorf("phaseOutcome() = %v", err)
			}
		})
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
			result := awaitBuild(t, testExecutor(t, f), testOptions())
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
			f := &fakeStubs{creates: buildCreates(1)}
			opts := testOptions()
			opts.Timeout = tt.timeout
			awaitBuild(t, testExecutor(t, f), opts)
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
		creates: append(buildCreates(0, 0, 0, 0), respond(completedOp("fetch", exitNoArtifact), nil)),
	}
	result := awaitBuild(t, testExecutor(t, f), testOptions())
	if !errors.Is(result.Error, ErrNoArtifact) {
		t.Fatalf("Result.Error = %v, want ErrNoArtifact", result.Error)
	}
}

func TestOversizeArtifactFailsBuild(t *testing.T) {
	f := &fakeStubs{
		creates: append(buildCreates(0, 0, 0, 0), respond(completedOp("fetch", exitArtifactTooBig), nil)),
	}
	result := awaitBuild(t, testExecutor(t, f), testOptions())
	if result.Error == nil || !strings.Contains(result.Error.Error(), "retrieval cap") {
		t.Fatalf("Result.Error = %v, want retrieval cap error", result.Error)
	}
}

func TestSuccessUploadsAssets(t *testing.T) {
	f := &fakeStubs{
		// No OutURI on the fetch op: an empty artifact.
		creates: append(buildCreates(0, 0, 0, 0), respond(completedOp("fetch", 0), nil)),
	}
	opts := testOptions()
	result := awaitBuild(t, testExecutor(t, f), opts)
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
	result := awaitBuild(t, testExecutor(t, f), testOptions())
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	want := &rebuild.BuildTimings{Setup: time.Second, Source: 2 * time.Second, Deps: 3 * time.Second, Build: 4 * time.Second}
	if diff := cmp.Diff(want, result.Timings); diff != "" {
		t.Errorf("Timings mismatch (-want +got):\n%s", diff)
	}
}

func TestTimingsAbsentWithoutWorkerStamps(t *testing.T) {
	// completedOp carries no StartedAt/FinishedAt: blind finalization. The
	// build succeeds but the timing record is incomplete.
	f := &fakeStubs{creates: append(buildCreates(0, 0, 0, 0), respond(completedOp("fetch", 0), nil))}
	result := awaitBuild(t, testExecutor(t, f), testOptions())
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	if result.Timings != nil {
		t.Errorf("Expected nil timings without worker stamps, got %+v", result.Timings)
	}
}

func TestAuthHeaderViaEnvOnly(t *testing.T) {
	f := &fakeStubs{creates: buildCreates(1)}
	e, err := NewExecutor(ExecutorConfig{
		ScratchID:    "s1",
		Stubs:        f.stubs(),
		GCSClient:    testGCSClient,
		PollInterval: time.Millisecond,
		AuthHeader: func(context.Context) (string, error) {
			return "Authorization: Bearer sekrit", nil
		},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
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
	f := &fakeStubs{creates: buildCreates(1)}
	e, err := NewExecutor(ExecutorConfig{
		ScratchID:       "s1",
		Stubs:           f.stubs(),
		GCSClient:       testGCSClient,
		PollInterval:    time.Millisecond,
		RetainContainer: true,
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	awaitBuild(t, e, testOptions())
	if start := f.createReqs[1]; slices.Contains(start.Cmd, "--rm") {
		t.Errorf("start cmd carries --rm, want retained container: %v", start.Cmd)
	}
}

func TestStartRejectsContainerImageOptions(t *testing.T) {
	e := testExecutor(t, &fakeStubs{})
	for _, tt := range []struct {
		name string
		mod  func(*build.Options)
	}{
		{name: "SaveContainerImage", mod: func(o *build.Options) { o.SaveContainerImage = true }},
		{name: "SavePostBuildContainer", mod: func(o *build.Options) { o.SavePostBuildContainer = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := testOptions()
			tt.mod(&opts)
			if _, err := e.Start(context.Background(), testInput(), opts); err == nil {
				t.Error("Start succeeded, want unsupported-option error")
			}
		})
	}
}

func TestStartRejectsUnsafeBuildID(t *testing.T) {
	e := testExecutor(t, &fakeStubs{})
	opts := testOptions()
	opts.BuildID = "bad id; rm -rf /"
	if _, err := e.Start(context.Background(), testInput(), opts); err == nil {
		t.Error("Start succeeded, want invalid build ID error")
	}
}

func TestExecPollsToTerminal(t *testing.T) {
	pending := &longrunning.Operation[schema.ScratchExecResult]{
		ID:     "op1",
		Result: &schema.ScratchExecResult{ScratchID: "s1"},
	}
	f := &fakeStubs{
		creates: []createFn{respond(pending, nil)},
		gets: map[string][]*longrunning.Operation[schema.ScratchExecResult]{
			"op1": {pending, completedOp("op1", 0)},
		},
	}
	op, err := Exec(context.Background(), f.stubs(), schema.ScratchExecRequest{
		ScratchID:      "s1",
		Cmd:            []string{"true"},
		TimeoutSeconds: 5,
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !op.Done || op.Result.ExitCode != 0 {
		t.Errorf("op = %+v, want done with exit 0", op)
	}
	if remaining := f.gets["op1"]; len(remaining) != 0 {
		t.Errorf("expected all scripted gets consumed, %d remaining", len(remaining))
	}
}

func TestNewlineFilteringReader(t *testing.T) {
	in := "aGVsbG8g\nd29ybGQ=\r\n"
	dec := base64.NewDecoder(base64.StdEncoding, newlineFilteringReader{r: strings.NewReader(in)})
	out, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out) != "hello world" {
		t.Errorf("decoded = %q, want %q", out, "hello world")
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
