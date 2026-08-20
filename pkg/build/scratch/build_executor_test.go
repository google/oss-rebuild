// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// buildVariantCreates scripts successful prepare, image build, and container
// run ops plus the post-run cleanup.
func buildVariantCreates() []createFn {
	return []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("image-build", 0), nil),
		respond(completedOp("container-run", 0), nil),
		respond(completedOp("cleanup", 0), nil),
	}
}

func testBuildExecutor(t *testing.T, f *fakeStubs) *DockerBuildExecutor {
	t.Helper()
	e, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{ExecutorConfig: testExecutorConfig(f)})
	if err != nil {
		t.Fatalf("NewDockerBuildExecutor: %v", err)
	}
	return e
}

func TestBuildDispatchesImageBuildAndRun(t *testing.T) {
	f := &fakeStubs{
		// No OutURI on the fetch op: an empty artifact.
		creates: append(buildVariantCreates(), respond(completedOp("fetch", 0), nil)),
	}
	opts := testOptions()
	result := awaitBuild(t, testBuildExecutor(t, f), opts)
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	if len(f.createReqs) != 5 {
		t.Fatalf("got %d exec creates, want 5 (prepare, image build, run, cleanup, fetch)", len(f.createReqs))
	}
	// Prepare creates the output mount point and sweeps prior containers
	// and images.
	prepareScript := f.createReqs[0].Cmd[len(f.createReqs[0].Cmd)-1]
	for _, want := range []string{"mkdir -p", "docker rm -f", "docker rmi -f"} {
		if !strings.Contains(prepareScript, want) {
			t.Errorf("prepare script missing %q:\n%s", want, prepareScript)
		}
	}
	// The image build feeds the Dockerfile through the exec stdin channel.
	imageBuild := f.createReqs[1]
	if want := []string{"docker", "buildx", "build", "-t", "rb-iter-1", "-"}; !slices.Equal(imageBuild.Cmd, want) {
		t.Errorf("image build cmd = %v, want %v", imageBuild.Cmd, want)
	}
	dockerfile, err := base64.StdEncoding.DecodeString(imageBuild.StdinB64)
	if err != nil {
		t.Fatalf("image build stdin undecodable: %v", err)
	}
	for _, want := range []string{"FROM ", "git clone", "npm install", "npm pack"} {
		if !strings.Contains(string(dockerfile), want) {
			t.Errorf("Dockerfile missing %q:\n%s", want, dockerfile)
		}
	}
	// The run mounts the staging directory over the plan's output directory
	// with the image last in the argv, and auto-removes the container.
	run := f.createReqs[2]
	wantPrefix := []string{"docker", "run", "--name", "rb-iter-1", "--rm"}
	if len(run.Cmd) < len(wantPrefix) || !slices.Equal(run.Cmd[:len(wantPrefix)], wantPrefix) {
		t.Errorf("run cmd = %v, want %v prefix", run.Cmd, wantPrefix)
	}
	if got := run.Cmd[len(run.Cmd)-1]; got != "rb-iter-1" {
		t.Errorf("run cmd ends with %q, want image tag last", got)
	}
	if i := slices.Index(run.Cmd, "-v"); i < 0 || !strings.HasSuffix(run.Cmd[i+1], ":/out") {
		t.Errorf("run cmd missing staging mount onto /out: %v", run.Cmd)
	}
	// Cleanup removes the image; the container was reclaimed by --rm.
	cleanupScript := f.createReqs[3].Cmd[len(f.createReqs[3].Cmd)-1]
	if !strings.Contains(cleanupScript, "docker rmi") {
		t.Errorf("cleanup script missing image removal:\n%s", cleanupScript)
	}
	if strings.Contains(cleanupScript, "docker rm -f") {
		t.Errorf("cleanup script removes the auto-removed container:\n%s", cleanupScript)
	}
	// The fetch targets the plan's artifact in the staging directory.
	fetchScript := f.createReqs[4].Cmd[len(f.createReqs[4].Cmd)-1]
	for _, want := range []string{"base64", "builds/iter-1/out/lodash-4.17.21.tgz"} {
		if !strings.Contains(fetchScript, want) {
			t.Errorf("fetch script missing %q:\n%s", want, fetchScript)
		}
	}
	// The artifact, logs, and Dockerfile all land in the asset store.
	for _, asset := range []rebuild.Asset{rebuild.RebuildAsset.For(testTarget), rebuild.DebugLogsAsset.For(testTarget), rebuild.DockerfileAsset.For(testTarget)} {
		r, err := opts.Resources.AssetStore.Reader(context.Background(), asset)
		if err != nil {
			t.Errorf("missing asset %v: %v", asset, err)
			continue
		}
		r.Close()
	}
}

func TestBuildImageBuildFailureSkipsRun(t *testing.T) {
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("image-build", 7), nil),
	}}
	result := awaitBuild(t, testBuildExecutor(t, f), testOptions())
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Code != 7 || exitErr.Phase != "image build" {
		t.Fatalf("Result.Error = %v, want ExitError{7, image build}", result.Error)
	}
	// Neither the run nor cleanup dispatch: there is no container or image.
	if len(f.createReqs) != 2 {
		t.Fatalf("got %d exec creates, want 2 (prepare, image build)", len(f.createReqs))
	}
}

func TestBuildContainerRunFailureCleansUp(t *testing.T) {
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("image-build", 0), nil),
		respond(completedOp("container-run", 3), nil),
		respond(completedOp("cleanup", 0), nil),
	}}
	result := awaitBuild(t, testBuildExecutor(t, f), testOptions())
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Code != 3 || exitErr.Phase != "container run" {
		t.Fatalf("Result.Error = %v, want ExitError{3, container run}", result.Error)
	}
	// The image is still reclaimed after a failed run.
	if len(f.createReqs) != 4 {
		t.Fatalf("got %d exec creates, want 4 (prepare, image build, run, cleanup)", len(f.createReqs))
	}
	if cleanup := f.createReqs[3].Cmd[len(f.createReqs[3].Cmd)-1]; !strings.Contains(cleanup, "docker rmi") {
		t.Errorf("cleanup script missing image removal:\n%s", cleanup)
	}
}

func TestBuildAuthSecretViaEnvOnly(t *testing.T) {
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("image-build", 1), nil),
	}}
	cfg := testExecutorConfig(f)
	cfg.AuthHeader = func(context.Context) (string, error) {
		return "Authorization: Bearer sekrit", nil
	}
	e, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{ExecutorConfig: cfg})
	if err != nil {
		t.Fatalf("NewDockerBuildExecutor: %v", err)
	}
	opts := testOptions()
	opts.Resources.ToolAuthRequired = []string{"gs://test-bootstrap"}
	awaitBuild(t, e, opts)
	// The image build receives the header via op env and consumes it
	// through an env-sourced secret, keeping the value out of the argv.
	imageBuild := f.createReqs[1]
	if imageBuild.Env["AUTH_HEADER"] != "Authorization: Bearer sekrit" {
		t.Errorf("image build Env = %v, want AUTH_HEADER set", imageBuild.Env)
	}
	if i := slices.Index(imageBuild.Cmd, "--secret"); i < 0 || imageBuild.Cmd[i+1] != "id=auth_header,env=AUTH_HEADER" {
		t.Errorf("image build cmd missing env-sourced secret: %v", imageBuild.Cmd)
	}
	for i, arg := range imageBuild.Cmd {
		if strings.Contains(arg, "sekrit") {
			t.Errorf("image build argv[%d] leaks the auth value: %q", i, arg)
		}
	}
}

func TestBuildRecordTimingsRetainsContainerForObservation(t *testing.T) {
	timedOp := func(id string) *longrunning.Operation[schema.ScratchExecResult] {
		op := completedOp(id, 0)
		op.Result.StartedAt = time.Unix(1700000000, 0)
		op.Result.FinishedAt = op.Result.StartedAt.Add(time.Minute)
		return op
	}
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(timedOp("image-build"), nil),
		respond(timedOp("container-run"), nil),
		// The history op carries no OutURI, so extraction stops there and
		// no timings are reported.
		respond(completedOp("history", 0), nil),
		respond(completedOp("cleanup", 0), nil),
		respond(completedOp("fetch", 0), nil),
	}}
	opts := testOptions()
	opts.RecordTimings = true
	result := awaitBuild(t, testBuildExecutor(t, f), opts)
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	if result.Timings != nil {
		t.Errorf("Expected nil timings without history output, got %+v", result.Timings)
	}
	if len(f.createReqs) != 6 {
		t.Fatalf("got %d exec creates, want 6 (prepare, image build, run, history, cleanup, fetch)", len(f.createReqs))
	}
	// The observed container is named, not auto-removed, and only
	// reclaimed by the post-observation cleanup.
	run := f.createReqs[2]
	if slices.Contains(run.Cmd, "--rm") {
		t.Errorf("run cmd carries --rm, want container retained for observation: %v", run.Cmd)
	}
	if history, want := f.createReqs[3].Cmd[:2], []string{"docker", "history"}; !slices.Equal(history, want) {
		t.Errorf("post-run cmd = %v, want docker history argv", f.createReqs[3].Cmd)
	}
	cleanup := f.createReqs[4].Cmd[len(f.createReqs[4].Cmd)-1]
	for _, want := range []string{"docker rm -f", "docker rmi"} {
		if !strings.Contains(cleanup, want) {
			t.Errorf("cleanup script missing %q:\n%s", want, cleanup)
		}
	}
}

func TestBuildFailedRunStillObserves(t *testing.T) {
	timedOp := func(id string) *longrunning.Operation[schema.ScratchExecResult] {
		op := completedOp(id, 0)
		op.Result.StartedAt = time.Unix(1700000000, 0)
		op.Result.FinishedAt = op.Result.StartedAt.Add(time.Minute)
		return op
	}
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(timedOp("image-build"), nil),
		respond(completedOp("container-run", 3), nil),
		// The history op carries no OutURI so extraction yields nothing.
		// Dispatching observation at all on a failed run is the property
		// under test.
		respond(completedOp("history", 0), nil),
		respond(completedOp("cleanup", 0), nil),
	}}
	opts := testOptions()
	opts.RecordTimings = true
	result := awaitBuild(t, testBuildExecutor(t, f), opts)
	var exitErr *ExitError
	if !errors.As(result.Error, &exitErr) || exitErr.Phase != "container run" {
		t.Fatalf("Result.Error = %v, want ExitError in container run", result.Error)
	}
	if result.Timings != nil {
		t.Errorf("Expected nil timings without history output, got %+v", result.Timings)
	}
	if len(f.createReqs) != 5 {
		t.Fatalf("got %d exec creates, want 5 (prepare, image build, run, history, cleanup)", len(f.createReqs))
	}
	if history, want := f.createReqs[3].Cmd[:2], []string{"docker", "history"}; !slices.Equal(history, want) {
		t.Errorf("post-run cmd = %v, want docker history argv", f.createReqs[3].Cmd)
	}
}

func TestBuildRetainFlagsSkipRemoval(t *testing.T) {
	f := &fakeStubs{creates: []createFn{
		respond(completedOp("prepare", 0), nil),
		respond(completedOp("image-build", 0), nil),
		respond(completedOp("container-run", 0), nil),
		respond(completedOp("fetch", 0), nil),
	}}
	cfg := testExecutorConfig(f)
	cfg.RetainContainer = true
	e, err := NewDockerBuildExecutor(DockerBuildExecutorConfig{ExecutorConfig: cfg, RetainImage: true})
	if err != nil {
		t.Fatalf("NewDockerBuildExecutor: %v", err)
	}
	result := awaitBuild(t, e, testOptions())
	if result.Error != nil {
		t.Fatalf("Result.Error = %v, want success", result.Error)
	}
	// With both retentions the cleanup exec is skipped entirely and the
	// run keeps the container.
	if len(f.createReqs) != 4 {
		t.Fatalf("got %d exec creates, want 4 (prepare, image build, run, fetch)", len(f.createReqs))
	}
	if run := f.createReqs[2]; slices.Contains(run.Cmd, "--rm") {
		t.Errorf("run cmd carries --rm, want retained container: %v", run.Cmd)
	}
}
