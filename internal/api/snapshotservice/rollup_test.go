// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshotservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSource struct {
	attempts []schema.RebuildAttempt
}

func (f *fakeSource) Attempts(context.Context, time.Time) ([]schema.RebuildAttempt, error) {
	return f.attempts, nil
}
func (f *fakeSource) Runs(context.Context, time.Time) ([]schema.Run, error) { return nil, nil }
func (f *fakeSource) Sessions(context.Context, time.Time) ([]schema.AgentSession, error) {
	return nil, nil
}
func (f *fakeSource) Iterations(context.Context, time.Time) ([]schema.AgentIteration, error) {
	return nil, nil
}
func (f *fakeSource) Scratches(context.Context, time.Time) ([]schema.Scratch, error) { return nil, nil }
func (f *fakeSource) Execs(context.Context, time.Time) ([]schema.ScratchExec, error) { return nil, nil }
func (f *fakeSource) RepoMetrics(context.Context, time.Time) ([]schema.RepoMetrics, error) {
	return nil, nil
}

func TestRollupUnconfigured(t *testing.T) {
	_, err := Rollup(context.Background(), RollupRequest{}, &RollupDeps{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code = %s; want FailedPrecondition. err=%v", status.Code(err), err)
	}
}

func TestRollupWritesSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := &fakeSource{attempts: []schema.RebuildAttempt{
		{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl", RunID: "r1", Success: true, Status: schema.RebuildStatusSuccess},
	}}
	deps := &RollupDeps{
		Source: src,
		Dest:   osfs.New(dir),
		Opts:   snapshot.Options{Project: "proj", ToolVersion: "test"},
	}
	resp, err := Rollup(ctx, RollupRequest{}, deps)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if resp.RowCounts[snapshot.TableAttempts] != 1 {
		t.Errorf("attempts count = %d, want 1", resp.RowCounts[snapshot.TableAttempts])
	}
	if resp.SchemaVersion != snapshot.SchemaVersion {
		t.Errorf("schema version = %d, want %d", resp.SchemaVersion, snapshot.SchemaVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, snapshot.Object)); err != nil {
		t.Errorf("snapshot database not written: %v", err)
	}
}
