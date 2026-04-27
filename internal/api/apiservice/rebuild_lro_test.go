// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/gcb/gcbtest"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/run/v2"
)

func TestCreateAndGetRebuildOp(t *testing.T) {
	ctx := context.Background()
	attempts := db.NewMemoryAttempts()

	deps := &CreateRebuildOpDeps{
		Attempts: attempts,
		RunJob: func(ctx context.Context, name string, req *run.GoogleCloudRunV2RunJobRequest) (*run.GoogleLongrunningOperation, error) {
			return &run.GoogleLongrunningOperation{Done: true}, nil
		},
	}

	req := schema.RebuildPackageRequest{
		Package:       "test-package",
		ID:            "run-id",
		Version:       "1.0.0",
		Artifact:      "test.tar.gz",
		ExecutionHint: schema.ExtendedExecution,
	}

	op, err := CreateRebuildOp(ctx, req, deps)
	if err != nil {
		t.Fatalf("CreateRebuildOp failed: %v", err)
	}

	if op.ID == "" {
		t.Error("expected non-empty operation ID")
	}

	if op.Done {
		t.Error("expected operation to be not done initially")
	}

	// Test Get
	getDeps := &GetRebuildOpDeps{
		Reader: NewRebuildView(attempts),
	}
	got, err := GetRebuildOp(ctx, schema.GetOperationRequest{ID: op.ID}, getDeps)
	if err != nil {
		t.Fatalf("GetRebuildOp failed: %v", err)
	}

	if got.ID != op.ID {
		t.Errorf("expected ID %s, got %s", op.ID, got.ID)
	}

	// Simulate completion by updating the underlying resource
	key, err := toAttemptKey(op.ID)
	if err != nil {
		t.Fatalf("toAttemptKey failed: %v", err)
	}
	attempt, err := attempts.Get(ctx, key)
	if err != nil {
		t.Fatalf("attempts.Get failed: %v", err)
	}
	attempt.Status = schema.RebuildStatusSuccess
	attempt.Message = "success"
	if err := attempts.Update(ctx, attempt); err != nil {
		t.Fatalf("attempts.Update failed: %v", err)
	}

	got, err = GetRebuildOp(ctx, schema.GetOperationRequest{ID: op.ID}, getDeps)
	if err != nil {
		t.Fatalf("GetRebuildOp failed: %v", err)
	}

	if !got.Done {
		t.Error("expected operation to be done")
	}
	if got.Result == nil || got.Result.Message != "success" {
		t.Errorf("expected success result, got %+v", got.Result)
	}
}

func TestCreateRebuildOpFast(t *testing.T) {
	ctx := context.Background()
	attempts := db.NewMemoryAttempts()

	target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "absl-py", Version: "2.0.0", Artifact: "absl_py-2.0.0-py3-none-any.whl"}
	calls := []httpxtest.Call{
		{
			URL: "https://pypi.org/pypi/absl-py/2.0.0/json",
			Response: &http.Response{
				StatusCode: 200,
				Body: httpxtest.Body(`{
              "info": {
                  "name": "absl-py",
                  "version": "2.0.0"
              },
              "urls": [
                  {
                      "filename": "absl_py-2.0.0-py3-none-any.whl",
                      "url": "https://files.pythonhosted.org/packages/01/e4/abcd.../absl_py-2.0.0-py3-none-any.whl"
                  }
              ]
          }`),
			},
		},
		{
			URL: "https://files.pythonhosted.org/packages/01/e4/abcd.../absl_py-2.0.0-py3-none-any.whl",
			Response: &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(must(archivetest.ZipFile([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
				}))),
			},
		},
	}
	strategy := &pypi.PureWheelBuild{
		Location: rebuild.Location{Repo: "https://github.com/abseil/abseil-py", Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Dir: "."},
	}
	file := must(archivetest.ZipFile([]archive.ZipEntry{
		{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
	}))

	// Replicate RebuildPackageDeps setup from TestRebuildPackage
	var d RebuildPackageDeps
	d.HTTPClient = &httpxtest.MockClient{
		Calls:        calls,
		URLValidator: httpxtest.NewURLValidator(t),
	}
	d.Signer = must(dsse.NewEnvelopeSigner(&FakeSigner{}))
	mfs := memfs.New()
	d.AttestationStore = rebuild.NewFilesystemAssetStore(must(mfs.Chroot("attestations")))
	d.DebugStoreBuilder = func(ctx context.Context) (rebuild.AssetStore, error) {
		return rebuild.NewFilesystemAssetStore(must(mfs.Chroot("debug-metadata"))), nil
	}
	remoteMetadata := rebuild.NewFilesystemAssetStore(must(mfs.Chroot("remote-metadata")))
	d.RemoteMetadataStoreBuilder = func(ctx context.Context, id string) (rebuild.LocatableAssetStore, error) {
		return remoteMetadata, nil
	}
	d.LocalMetadataStore = rebuild.NewFilesystemAssetStore(must(mfs.Chroot("local-metadata")))
	d.Attempts = attempts
	buildSteps := []*cloudbuild.BuildStep{
		{Name: "gcr.io/foo/bar", Script: "./bar"},
	}
	gcbclient := &gcbtest.MockClient{
		CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
			c := must(remoteMetadata.Writer(ctx, rebuild.RebuildAsset.For(target)))
			defer func() { must1(c.Close()) }()
			must(c.Write(file.Bytes()))
			return &cloudbuild.Operation{
				Name: "operations/build-id",
				Done: false,
				Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: &cloudbuild.Build{
					Id:     "build-id",
					Status: "QUEUED",
					Steps:  buildSteps,
				}})),
			}, nil
		},
		WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
			return &cloudbuild.Operation{
				Name: "operations/build-id",
				Done: true,
				Metadata: must(json.Marshal(cloudbuild.BuildOperationMetadata{Build: &cloudbuild.Build{
					Id:         "build-id",
					Status:     "SUCCESS",
					StartTime:  "2024-05-08T15:00:00Z",
					FinishTime: "2024-05-08T15:23:00Z",
					Steps:      buildSteps,
					Results:    &cloudbuild.Results{BuildStepImages: []string{"sha256:abcd"}},
				}})),
			}, nil
		},
	}
	d.GCBExecutor = must(buildgcb.NewExecutor(buildgcb.ExecutorConfig{
		Project:        "foo-project",
		ServiceAccount: "foo-role",
		Planner: buildgcb.NewPlanner(buildgcb.PlannerConfig{
			ServiceAccount:  "foo-role",
			AllowPrivileged: true,
		}),
		LogsBucket: "foo-logs-bucket",
		LogsClientFunc: func(bucket string) gcb.LogsClient {
			return &gcbtest.MockLogsClient{
				ReadBuildLogsFunc: func(ctx context.Context, buildID string) (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewBuffer(nil)), nil
				},
			}
		},
		Client: gcbclient,
	}))
	d.ServiceRepo = rebuild.Location{Repo: "https://github.internal/foo/repo", Ref: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Dir: "."}
	d.InferStub = func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
		oneof := schema.NewStrategyOneOf(strategy)
		return &oneof, nil
	}

	lroDeps := &CreateRebuildOpDeps{
		Attempts: attempts,
		DepsFunc: func(ctx context.Context, cfg *schema.RebuildDepsConfig) (*RebuildPackageDeps, error) {
			return &d, nil
		},
	}

	req := schema.RebuildPackageRequest{
		Ecosystem:     target.Ecosystem,
		Package:       target.Package,
		Version:       target.Version,
		Artifact:      target.Artifact,
		ID:            "test-run-fast",
		ExecutionHint: schema.FastExecution,
	}

	op, err := CreateRebuildOp(ctx, req, lroDeps)
	if err != nil {
		t.Fatalf("CreateRebuildOp failed: %v", err)
	}

	if !op.Done {
		t.Error("expected operation to be done for FastExecution")
	}
	if op.Result == nil {
		t.Fatal("expected operation result to be set")
	}
	if op.Result.Target != target {
		t.Errorf("expected target %v, got %v", target, op.Result.Target)
	}

	// Verify DB state
	key := db.AttemptKey{Target: target, RunID: req.ID}
	got, err := attempts.Get(ctx, key)
	if err != nil {
		t.Fatalf("attempts.Get failed: %v", err)
	}
	if got.Status != schema.RebuildStatusSuccess {
		t.Errorf("expected status SUCCESS, got %s", got.Status)
	}
}
