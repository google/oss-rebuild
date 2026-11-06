// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

var (
	target = rebuild.Target{Ecosystem: rebuild.PyPI, Package: "absl-py", Version: "2.0.0", Artifact: "absl_py-2.0.0-py3-none-any.whl"}
	req    = schema.RebuildPackageRequest{Ecosystem: target.Ecosystem, Package: target.Package, Version: target.Version, Artifact: target.Artifact, ID: "run"}
)

// failingUpsert fails the first write of a rebuild.
type failingUpsert struct{ db.Attempts }

func (failingUpsert) Upsert(context.Context, schema.RebuildAttempt) error { return io.EOF }

func depsOf(d *apiservice.RebuildPackageDeps, err error) depsFunc {
	return func(schema.RebuildDepsConfig) (*apiservice.RebuildPackageDeps, error) { return d, err }
}

func TestRunRetriesWhatDidNotStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps depsFunc
	}{
		{name: "NoDeps", deps: depsOf(nil, io.EOF)},
		{name: "FirstWriteFails", deps: depsOf(&apiservice.RebuildPackageDeps{Attempts: failingUpsert{db.NewMemoryAttempts()}}, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(context.Background(), apiservice.BuildTask{OpID: "op", Request: req}, tc.deps); err == nil {
				t.Error("run() = nil, want an error to retry")
			}
		})
	}
}

func TestRunRecordsFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		timeout    time.Duration
		infer      func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error)
		wantStatus schema.RebuildStatus
	}{
		{
			name: "InferenceFails",
			infer: func(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
				return nil, io.EOF
			},
			wantStatus: schema.RebuildStatusError,
		},
		{
			name:    "BuildTimeout",
			timeout: time.Millisecond,
			infer: func(ctx context.Context, _ schema.InferenceRequest) (*schema.StrategyOneOf, error) {
				if _, ok := ctx.Deadline(); !ok {
					return nil, io.EOF // Waiting on ctx would hang.
				}
				<-ctx.Done()
				return nil, ctx.Err()
			},
			wantStatus: schema.RebuildStatusCancelled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			attempts := db.NewMemoryAttempts()
			// LocalMetadataStore is nil, so a rebuild that used the shared one panics.
			shared := &apiservice.RebuildPackageDeps{
				Attempts:         attempts,
				AttestationStore: rebuild.NewFilesystemAssetStore(memfs.New()),
				InferStub:        tc.infer,
				InferVersionStub: func(context.Context, schema.VersionRequest) (*schema.VersionResponse, error) {
					return &schema.VersionResponse{Version: "v1"}, nil
				},
			}
			timed := req
			timed.BuildTimeout = tc.timeout
			if err := run(ctx, apiservice.BuildTask{OpID: "op", Request: timed}, depsOf(shared, nil)); err != nil {
				t.Fatalf("run() = %v, want nil for a recorded failure", err)
			}
			got, err := attempts.Get(ctx, db.AttemptKey{Target: target, RunID: req.ID})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus || got.Finished.IsZero() {
				t.Errorf("attempt %s, finished %v; want %s", got.Status, got.Finished, tc.wantStatus)
			}
			if shared.LocalMetadataStore != nil {
				t.Error("rebuild replaced the shared deps' metadata store")
			}
		})
	}
}
