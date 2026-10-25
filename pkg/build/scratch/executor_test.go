// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/memfs"
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

func testExecutorConfig(f *fakeStubs) ExecutorConfig {
	return ExecutorConfig{
		ScratchID:    "s1",
		Stubs:        f.stubs(),
		GCSClient:    testGCSClient,
		PollInterval: time.Millisecond,
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

func awaitBuild(t *testing.T, e build.Executor, opts build.Options) build.Result {
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

func TestPhaseOutcome(t *testing.T) {
	isInfra := func(err error) bool {
		return err != nil && !errors.As(err, new(*ExitError)) && !errors.Is(err, context.DeadlineExceeded)
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

func TestStartRejectsContainerImageOptions(t *testing.T) {
	e := testRunExecutor(t, &fakeStubs{})
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
	e := testRunExecutor(t, &fakeStubs{})
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

// ctxStore commits a writer's bytes on Close only while its context is
// live, as the GCS writer does.
type ctxStore struct {
	rebuild.AssetStore
	committed [][]byte
}

func (s *ctxStore) Writer(ctx context.Context, _ rebuild.Asset) (io.WriteCloser, error) {
	return &ctxWriter{ctx: ctx, store: s}, nil
}

type ctxWriter struct {
	ctx   context.Context
	store *ctxStore
	buf   bytes.Buffer
}

func (w *ctxWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *ctxWriter) Close() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.store.committed = append(w.store.committed, w.buf.Bytes())
	return nil
}

func TestUploadStream(t *testing.T) {
	asset := rebuild.RebuildAsset.For(testTarget)
	e := &executor{}
	t.Run("Committed", func(t *testing.T) {
		store := &ctxStore{}
		if err := e.uploadStream(context.Background(), store, asset, strings.NewReader("artifact")); err != nil {
			t.Fatalf("uploadStream: %v", err)
		}
		if len(store.committed) != 1 || string(store.committed[0]) != "artifact" {
			t.Errorf("committed %q, want [artifact]", store.committed)
		}
	})
	t.Run("AbandonedOnReadError", func(t *testing.T) {
		store := &ctxStore{}
		r := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("connection reset")))
		err := e.uploadStream(context.Background(), store, asset, r)
		if err == nil || !strings.Contains(err.Error(), "writing asset") {
			t.Fatalf("uploadStream() = %v, want writing asset error", err)
		}
		if len(store.committed) != 0 {
			t.Errorf("partial upload committed %q, want abandoned", store.committed)
		}
	})
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
