// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshotservice

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSource struct {
	attempts []schema.RebuildAttempt
}

func (f *fakeSource) Attempts(context.Context, time.Time) iter.Seq2[schema.RebuildAttempt, error] {
	return iterx.FromSlice(f.attempts)
}
func (f *fakeSource) Runs(context.Context, time.Time) iter.Seq2[schema.Run, error] {
	return iterx.FromSlice[schema.Run](nil)
}
func (f *fakeSource) Sessions(context.Context, time.Time) iter.Seq2[schema.AgentSession, error] {
	return iterx.FromSlice[schema.AgentSession](nil)
}
func (f *fakeSource) Iterations(context.Context, time.Time) iter.Seq2[schema.AgentIteration, error] {
	return iterx.FromSlice[schema.AgentIteration](nil)
}
func (f *fakeSource) Scratches(context.Context, time.Time) iter.Seq2[schema.Scratch, error] {
	return iterx.FromSlice[schema.Scratch](nil)
}
func (f *fakeSource) Execs(context.Context, time.Time) iter.Seq2[schema.ScratchExec, error] {
	return iterx.FromSlice[schema.ScratchExec](nil)
}
func (f *fakeSource) RepoMetrics(context.Context, time.Time) iter.Seq2[schema.RepoMetrics, error] {
	return iterx.FromSlice[schema.RepoMetrics](nil)
}
func (f *fakeSource) Campaigns(context.Context, time.Time) iter.Seq2[scheduler.Campaign, error] {
	return iterx.FromSlice[scheduler.Campaign](nil)
}
func (f *fakeSource) Signals(context.Context) (iter.Seq2[signals.PackageSignal, error], time.Time, error) {
	return iterx.FromSlice[signals.PackageSignal](nil), time.Time{}, nil
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
	if resp.RowCounts[snapshot.TablePackageStats] != 1 {
		t.Errorf("package_stats count = %d, want 1", resp.RowCounts[snapshot.TablePackageStats])
	}
	if resp.SchemaVersion != snapshot.SchemaVersion {
		t.Errorf("schema version = %d, want %d", resp.SchemaVersion, snapshot.SchemaVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, snapshot.Object)); err != nil {
		t.Errorf("snapshot database not written: %v", err)
	}
}
