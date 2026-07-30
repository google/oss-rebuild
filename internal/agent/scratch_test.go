// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"io"
	"strings"
	"testing"

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
	oneof := schema.NewStrategyOneOf(&rebuild.ManualStrategy{
		Location: rebuild.Location{
			Repo: "https://github.com/lodash/lodash",
			Ref:  "aa1d7d870d9cf84842ee23ff485fd24abf0ed3d1",
			Dir:  ".",
		},
		Deps:       "npm install",
		Build:      "npm pack",
		OutputPath: "lodash-4.17.21.tgz",
	})
	return &oneof
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
	r := testRunner(&fakeExecutor{})
	r.Stubs = scratch.Stubs{
		ExecCreate: func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			gotReq = req
			return &longrunning.Operation[schema.ScratchExecResult]{
				ID:     "cmd",
				Done:   true,
				Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: 3},
			}, nil
		},
		ExecGet: func(_ context.Context, req schema.GetOperationRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
			return nil, errors.New("unexpected ExecGet")
		},
	}
	exitCode, _, err := r.RunCommand(context.Background(), "docker images", 0)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if exitCode != 3 {
		t.Errorf("exitCode = %d, want 3", exitCode)
	}
	if len(gotReq.Cmd) != 3 || gotReq.Cmd[2] != "docker images" {
		t.Errorf("req.Cmd = %v, want /bin/sh -c 'docker images'", gotReq.Cmd)
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
	exitCode, _, err := r.RunCommand(context.Background(), "sleep 600", 5)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout error", err)
	}
	if exitCode != 124 {
		t.Errorf("exitCode = %d, want 124", exitCode)
	}
}
