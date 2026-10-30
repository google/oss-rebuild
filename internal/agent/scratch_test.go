// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcsx/gcsxtest"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

var testTarget = rebuild.Target{
	Ecosystem: rebuild.NPM,
	Package:   "lodash",
	Version:   "4.17.21",
	Artifact:  "lodash-4.17.21.tgz",
}

func testStrategy() *schema.StrategyOneOf {
	return new(schema.NewStrategyOneOf(&rebuild.ManualStrategy{
		Location: rebuild.Location{
			Repo: "https://github.com/lodash/lodash",
			Ref:  "aa1d7d870d9cf84842ee23ff485fd24abf0ed3d1",
			Dir:  ".",
		},
		Deps:       "npm install",
		Build:      "npm pack",
		OutputPath: "lodash-4.17.21.tgz",
	}))
}

type fakeHandle struct {
	result build.Result
	err    error
}

func (h *fakeHandle) BuildID() string                            { return "fake" }
func (h *fakeHandle) Wait(context.Context) (build.Result, error) { return h.result, h.err }
func (h *fakeHandle) OutputStream() io.Reader                    { return strings.NewReader("") }
func (h *fakeHandle) Status() build.BuildState                   { return build.BuildStateCompleted }

// fakeExecutor returns a scripted result and optionally stages debug logs
// and a rebuilt artifact into the provided asset store (as the scratch
// executor would).
type fakeExecutor struct {
	startErr error
	result   build.Result
	waitErr  error
	logs     []byte
	artifact []byte
}

func (e *fakeExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	if e.startErr != nil {
		return nil, e.startErr
	}
	stage := func(asset rebuild.Asset, content []byte) error {
		if content == nil || opts.Resources.AssetStore == nil {
			return nil
		}
		w, err := opts.Resources.AssetStore.Writer(ctx, asset)
		if err != nil {
			return err
		}
		if _, err := w.Write(content); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	if err := stage(rebuild.DebugLogsAsset.For(input.Target), e.logs); err != nil {
		return nil, err
	}
	if err := stage(rebuild.RebuildAsset.For(input.Target), e.artifact); err != nil {
		return nil, err
	}
	return &fakeHandle{result: e.result, err: e.waitErr}, nil
}

func (e *fakeExecutor) Status() build.ExecutorStatus { return build.ExecutorStatus{Healthy: true} }
func (e *fakeExecutor) Close(context.Context) error  { return nil }

func testRunner(executor build.Executor) *ScratchRunner {
	return &ScratchRunner{
		Target:         testTarget,
		Executor:       executor,
		ScratchID:      "s1",
		PrebuildConfig: rebuild.PrebuildConfig{Bucket: "test-bootstrap"},
	}
}

func TestRunClassification(t *testing.T) {
	for _, tt := range []struct {
		name       string
		executor   *fakeExecutor
		wantStatus string
		wantMsg    string
	}{
		{
			name:       "nonzero exit is failed",
			executor:   &fakeExecutor{result: build.Result{Error: &scratch.ExitError{Code: 42}}},
			wantStatus: schema.AgentIterationStatusFailed,
			wantMsg:    "exit code 42",
		},
		{
			name:       "timeout is failed",
			executor:   &fakeExecutor{result: build.Result{Error: errors.Wrap(context.DeadlineExceeded, "build timed out after 3600s")}},
			wantStatus: schema.AgentIterationStatusFailed,
			wantMsg:    "timed out",
		},
		{
			name:       "output cap is failed",
			executor:   &fakeExecutor{result: build.Result{Error: errors.Wrap(scratch.ErrOutputCapped, "build phase: output exceeded 268435456 bytes")}},
			wantStatus: schema.AgentIterationStatusFailed,
			wantMsg:    "output cap",
		},
		{
			name:       "missing artifact is failed",
			executor:   &fakeExecutor{result: build.Result{Error: scratch.ErrNoArtifact}},
			wantStatus: schema.AgentIterationStatusFailed,
			wantMsg:    "no artifact",
		},
		{
			name:       "infrastructure failure is error",
			executor:   &fakeExecutor{result: build.Result{Error: errors.New("build exec lost: worker dispatch failed")}},
			wantStatus: schema.AgentIterationStatusError,
		},
		{
			name:       "start failure is error",
			executor:   &fakeExecutor{startErr: errors.New("scratch not found")},
			wantStatus: schema.AgentIterationStatusError,
		},
		{
			name:       "wait failure is error",
			executor:   &fakeExecutor{waitErr: context.Canceled},
			wantStatus: schema.AgentIterationStatusError,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := testRunner(tt.executor)
			status, result := r.Run(context.Background(), "iter-1", testStrategy())
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if result.BuildSuccess {
				t.Error("result.BuildSuccess = true, want false")
			}
			if tt.wantMsg != "" && !strings.Contains(result.ErrorMessage, tt.wantMsg) {
				t.Errorf("result.ErrorMessage = %q, want substring %q", result.ErrorMessage, tt.wantMsg)
			}
		})
	}
}

func TestLastBuildLogs(t *testing.T) {
	r := testRunner(&fakeExecutor{
		result: build.Result{Error: &scratch.ExitError{Code: 1}},
		logs:   []byte("npm ERR! missing script: build"),
	})
	if _, err := r.LastBuildLogs(context.Background()); err == nil {
		t.Error("LastBuildLogs before any run succeeded, want error")
	}
	r.Run(context.Background(), "iter-1", testStrategy())
	logs, err := r.LastBuildLogs(context.Background())
	if err != nil {
		t.Fatalf("LastBuildLogs: %v", err)
	}
	if !strings.Contains(string(logs), "npm ERR!") {
		t.Errorf("logs = %q, want build output", logs)
	}
}

func TestRunCommand(t *testing.T) {
	var gotReq schema.ScratchExecRequest
	started := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	r := testRunner(&fakeExecutor{})
	r.Stubs = scratch.Stubs{
		ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			gotReq = req
			return &longrunning.Operation[schema.ScratchExecResult]{
				ID:   "cmd",
				Done: true,
				Result: &schema.ScratchExecResult{
					ScratchID:  "s1",
					ExitCode:   3,
					StartedAt:  started,
					FinishedAt: started.Add(time.Second),
				},
			}, nil
		},
		ExecGet: func(_ context.Context, req schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			return nil, errors.New("unexpected ExecGet")
		},
	}
	res, err := r.RunCommand(context.Background(), "docker images", 0, schema.ToolLabel("run_on_host"))
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exitCode = %d, want 3", res.ExitCode)
	}
	// The record written for the call names the exec and when it ran.
	if res.ExecID != "cmd" || !res.StartedAt.Equal(started) || !res.FinishedAt.Equal(started.Add(time.Second)) {
		t.Errorf("result = %+v, want the exec's ID and timings", res)
	}
	if len(gotReq.Cmd) != 3 || gotReq.Cmd[2] != "docker images" {
		t.Errorf("req.Cmd = %v, want /bin/sh -c 'docker images'", gotReq.Cmd)
	}
	if gotReq.Label != "tool/run_on_host" {
		t.Errorf("req.Label = %q, want tool/run_on_host", gotReq.Label)
	}
	if gotReq.ScratchID != "s1" {
		t.Errorf("req.ScratchID = %q, want s1", gotReq.ScratchID)
	}
	if gotReq.TimeoutSeconds != int(defaultCommandTimeout.Seconds()) {
		t.Errorf("req.TimeoutSeconds = %d, want default %d", gotReq.TimeoutSeconds, int(defaultCommandTimeout.Seconds()))
	}
}

func TestRunCommandTimeout(t *testing.T) {
	r := testRunner(&fakeExecutor{})
	r.Stubs = scratch.Stubs{
		ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			return &longrunning.Operation[schema.ScratchExecResult]{
				ID:     "cmd",
				Done:   true,
				Error:  &longrunning.OperationError{Code: int(codes.DeadlineExceeded), Message: "command exceeded TimeoutSeconds"},
				Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: 124},
			}, nil
		},
		ExecGet: func(_ context.Context, req schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			return nil, errors.New("unexpected ExecGet")
		},
	}
	res, err := r.RunCommand(context.Background(), "sleep 600", 5, schema.ToolLabel("run_on_host"))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout error", err)
	}
	if res.ExitCode != 124 {
		t.Errorf("exitCode = %d, want 124", res.ExitCode)
	}
}

// TestDiffOnVM covers the on-VM comparison exec: the script fetches the
// tools and the upstream artifact when absent, stabilizes both artifacts,
// then runs diffr --summary on the stabilized forms, and the exit code
// separates fetch and stabilize failures from diffr's own verdicts.
func TestDiffOnVM(t *testing.T) {
	for _, tt := range []struct {
		name     string
		exitCode int
		auth     bool
		want     string
		wantErr  string
	}{
		{name: "differ", exitCode: 1, auth: true},
		{name: "same after stabilization", exitCode: 0, want: "no file-level differences"},
		{name: "setup failed", exitCode: exitDiffSetupFailed, wantErr: "preparing the comparison on the VM"},
		{name: "diffr error", exitCode: 127, wantErr: "diffr exited 127"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq schema.ScratchExecRequest
			r := testRunner(&fakeExecutor{})
			r.PrebuildConfig.Dir = "v1"
			if tt.auth {
				r.AuthHeader = func(context.Context) (string, error) { return "Authorization: Bearer tok", nil }
			}
			r.Stubs = scratch.Stubs{
				ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
					gotReq = req
					return &longrunning.Operation[schema.ScratchExecResult]{ID: "diff", Done: true, Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: tt.exitCode}}, nil
				},
				ExecGet: func(context.Context, schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
					return nil, errors.New("unexpected ExecGet")
				},
			}
			got, err := r.diffOnVM(context.Background(), "iter-1", "https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("diffOnVM: %v", err)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("summary = %q, want substring %q", got, tt.want)
			}
			want := `set -eu
die() { echo "$*"; exit 3; }
stab() { out=$(/home/builder/tools/stabilize --ecosystem npm --artifact lodash-4.17.21.tgz --infile "$1" --outfile "$2.part" 2>&1) && mv "$2.part" "$2" || die "stabilizing $1 failed: $out"; }
fetch() { [ -e "$1" ] || { wget -q ${3:+--header "$3"} -O "$1.part" "$2" && mv "$1.part" "$1"; } || die "fetching $2 failed"; }
mkdir -p /home/builder/tools /home/builder/upstream
fetch /home/builder/tools/diffr https://storage.googleapis.com/test-bootstrap/v1/diffr "${AUTH_HEADER:-}"
fetch /home/builder/tools/stabilize https://storage.googleapis.com/test-bootstrap/v1/stabilize "${AUTH_HEADER:-}"
fetch /home/builder/upstream/lodash-4.17.21.tgz https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz
chmod +x /home/builder/tools/diffr /home/builder/tools/stabilize
[ -e /home/builder/upstream/lodash-4.17.21.tgz.stabilized ] || stab /home/builder/upstream/lodash-4.17.21.tgz /home/builder/upstream/lodash-4.17.21.tgz.stabilized
stab /home/builder/builds/iter-1/out/rebuild /home/builder/builds/iter-1/out/rebuild.stabilized
exec /home/builder/tools/diffr --summary --label rebuild --label upstream /home/builder/builds/iter-1/out/rebuild.stabilized /home/builder/upstream/lodash-4.17.21.tgz.stabilized
`
			if diff := cmp.Diff(want, gotReq.Cmd[2]); diff != "" {
				t.Errorf("script mismatch (-want +got):\n%s", diff)
			}
			if _, ok := gotReq.Env["AUTH_HEADER"]; ok != tt.auth {
				t.Errorf("AUTH_HEADER env set = %v, want %v", ok, tt.auth)
			}
			if gotReq.TimeoutSeconds != int(diffTimeout.Seconds()) {
				t.Errorf("TimeoutSeconds = %d, want %d", gotReq.TimeoutSeconds, int(diffTimeout.Seconds()))
			}
		})
	}
}

// The staging exec fetches and stabilizes but compares nothing.
func TestPrepareCompare(t *testing.T) {
	for _, tt := range []struct {
		name     string
		exitCode int
		wantErr  string
	}{
		{name: "prepared"},
		{name: "setup failed", exitCode: exitDiffSetupFailed, wantErr: "preparing the comparison on the VM"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq schema.ScratchExecRequest
			r := testRunner(&fakeExecutor{})
			r.PrebuildConfig.Dir = "v1"
			r.RegistryClient = &fakeRegistry{}
			r.Stubs = scratch.Stubs{
				ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
					gotReq = req
					return &longrunning.Operation[schema.ScratchExecResult]{ID: "prep", Done: true, Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: tt.exitCode}}, nil
				},
			}
			err := r.PrepareCompare(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PrepareCompare: %v", err)
			}
			want := `set -eu
die() { echo "$*"; exit 3; }
stab() { out=$(/home/builder/tools/stabilize --ecosystem npm --artifact lodash-4.17.21.tgz --infile "$1" --outfile "$2.part" 2>&1) && mv "$2.part" "$2" || die "stabilizing $1 failed: $out"; }
fetch() { [ -e "$1" ] || { wget -q ${3:+--header "$3"} -O "$1.part" "$2" && mv "$1.part" "$1"; } || die "fetching $2 failed"; }
mkdir -p /home/builder/tools /home/builder/upstream
fetch /home/builder/tools/diffr https://storage.googleapis.com/test-bootstrap/v1/diffr "${AUTH_HEADER:-}"
fetch /home/builder/tools/stabilize https://storage.googleapis.com/test-bootstrap/v1/stabilize "${AUTH_HEADER:-}"
fetch /home/builder/upstream/lodash-4.17.21.tgz https://registry.example/lodash-4.17.21.tgz
chmod +x /home/builder/tools/diffr /home/builder/tools/stabilize
[ -e /home/builder/upstream/lodash-4.17.21.tgz.stabilized ] || stab /home/builder/upstream/lodash-4.17.21.tgz /home/builder/upstream/lodash-4.17.21.tgz.stabilized
`
			if diff := cmp.Diff(want, gotReq.Cmd[2]); diff != "" {
				t.Errorf("script mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMismatchMessage(t *testing.T) {
	r := testRunner(&fakeExecutor{})
	if got := r.mismatchMessage("iter-1", ""); got != "rebuild content mismatch" {
		t.Errorf("message without summary = %q", got)
	}
	got := r.mismatchMessage("iter-1", "within rebuild (vs upstream):\n  differ (1):\n    package/index.js\n")
	for _, want := range []string{
		"differ (1):\n    package/index.js",
		"only in rebuild, only in upstream, and differ",
		"/home/builder/tools/diffr /home/builder/builds/iter-1/out/rebuild.stabilized /home/builder/upstream/lodash-4.17.21.tgz.stabilized",
		"/home/builder/tools/stabilize --ecosystem npm --artifact lodash-4.17.21.tgz --infile <file> --outfile <file>.stabilized",
		"/src is the host directory /home/builder/builds/iter-1/src",
		"Do not write the upstream bytes into the tree",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
}

func TestMismatchMessageSingleEnv(t *testing.T) {
	r := testRunner(&fakeExecutor{})
	r.SingleEnv = true
	got := r.mismatchMessage("iter-1", "~ package/index.js\n")
	for _, want := range []string{
		"~ package/index.js",
		"run diffr with run_in_container",
		"/tools/diffr /out/rebuild.stabilized /upstream/lodash-4.17.21.tgz.stabilized",
		"/tools/stabilize --ecosystem npm --artifact lodash-4.17.21.tgz --infile <file> --outfile <file>.stabilized",
		"Your rebuild is at /out/rebuild",
		"/builds holds this and every earlier attempt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("single-env message lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"run_on_host", "/home/builder"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("single-env message should not mention %q:\n%s", unwanted, got)
		}
	}
}

func TestCompareScriptRejectsShellCharacters(t *testing.T) {
	r := testRunner(&fakeExecutor{})
	r.Target.Artifact = "it's-4.17.21.tgz"
	p := r.vmPaths("iter-1")
	if _, err := r.compareScript(p, "https://r/x.tgz", p.rebuild); err == nil {
		t.Error("compareScript accepted an artifact name with a quote")
	}
}

// The check fetches nothing and sends no prebuild credential.
func TestCheckArtifact(t *testing.T) {
	var gotReq schema.ScratchExecRequest
	r := testRunner(&fakeExecutor{})
	r.AuthHeader = func(context.Context) (string, error) { return "Authorization: Bearer tok", nil }
	r.Stubs = scratch.Stubs{
		ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			gotReq = req
			return &longrunning.Operation[schema.ScratchExecResult]{ID: "check", Done: true, Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: 1, OutURI: "gs://out/check"}}, nil
		},
		ExecGet: func(context.Context, schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			return nil, errors.New("unexpected ExecGet")
		},
	}
	r.GCSClient = gcsxtest.Client(t, map[string]string{
		"out/check": "ab cd /home/builder/checks/lodash-4.17.21.tgz.stabilized /home/builder/upstream/lodash-4.17.21.tgz.stabilized\n  differ (1):\n",
	})
	code, out, err := r.CheckArtifact(context.Background(), "/home/builder/builds/iter-1/src/lodash-4.17.21.tgz")
	if err != nil || code != 1 || !strings.HasPrefix(string(out), "ab cd ") {
		t.Fatalf("CheckArtifact = %d, %q, %v; want diffr's exit and output", code, out, err)
	}
	want := `set -eu
die() { echo "$*"; exit 3; }
stab() { out=$(/home/builder/tools/stabilize --ecosystem npm --artifact lodash-4.17.21.tgz --infile "$1" --outfile "$2.part" 2>&1) && mv "$2.part" "$2" || die "stabilizing $1 failed: $out"; }
[ -f /home/builder/builds/iter-1/src/lodash-4.17.21.tgz ] || die "/home/builder/builds/iter-1/src/lodash-4.17.21.tgz: no such file on the VM host"
for f in /home/builder/upstream/lodash-4.17.21.tgz /home/builder/tools/stabilize /home/builder/tools/diffr; do [ -e $f ] || die "$f is not on the VM; the job stages the upstream artifact and the tools before each session"; done
[ -e /home/builder/upstream/lodash-4.17.21.tgz.stabilized ] || stab /home/builder/upstream/lodash-4.17.21.tgz /home/builder/upstream/lodash-4.17.21.tgz.stabilized
mkdir -p /home/builder/checks
stab /home/builder/builds/iter-1/src/lodash-4.17.21.tgz /home/builder/checks/lodash-4.17.21.tgz.stabilized
c=$(sha256sum /home/builder/checks/lodash-4.17.21.tgz.stabilized | cut -c1-64)
u=$(sha256sum /home/builder/upstream/lodash-4.17.21.tgz.stabilized | cut -c1-64)
echo "$c $u /home/builder/checks/lodash-4.17.21.tgz.stabilized /home/builder/upstream/lodash-4.17.21.tgz.stabilized"
[ "$c" != "$u" ] || exit 0
exec /home/builder/tools/diffr --summary --label candidate --label upstream /home/builder/checks/lodash-4.17.21.tgz.stabilized /home/builder/upstream/lodash-4.17.21.tgz.stabilized
`
	if diff := cmp.Diff(want, gotReq.Cmd[2]); diff != "" {
		t.Errorf("script mismatch (-want +got):\n%s", diff)
	}
	if _, ok := gotReq.Env["AUTH_HEADER"]; ok || gotReq.TimeoutSeconds != int(diffTimeout.Seconds()) {
		t.Errorf("env = %v, timeout = %d; want no credential and the comparison timeout", gotReq.Env, gotReq.TimeoutSeconds)
	}
	if _, _, err := r.CheckArtifact(context.Background(), "/home/builder/builds/iter-1/src/x;rm -rf /"); err == nil || !strings.Contains(err.Error(), "not a shell word") {
		t.Errorf("CheckArtifact with shell characters = %v, want a refusal", err)
	}
}
