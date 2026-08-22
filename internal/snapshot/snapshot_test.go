// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/ncruces/go-sqlite3"
)

// fakeSource serves fixed fixtures, isolating the snapshot writer/derivation
// pipeline from Firestore. It records the watermark of each attempts scan.
type fakeSource struct {
	since       []time.Time
	attempts    []schema.RebuildAttempt
	runs        []schema.Run
	sessions    []schema.AgentSession
	iterations  []schema.AgentIteration
	scratches   []schema.Scratch
	execs       []schema.ScratchExec
	repoMetrics []schema.RepoMetrics
}

func (f *fakeSource) Attempts(_ context.Context, since time.Time) ([]schema.RebuildAttempt, error) {
	f.since = append(f.since, since)
	return f.attempts, nil
}
func (f *fakeSource) Runs(context.Context, time.Time) ([]schema.Run, error) { return f.runs, nil }
func (f *fakeSource) Sessions(context.Context, time.Time) ([]schema.AgentSession, error) {
	return f.sessions, nil
}
func (f *fakeSource) Iterations(context.Context, time.Time) ([]schema.AgentIteration, error) {
	return f.iterations, nil
}
func (f *fakeSource) Scratches(context.Context, time.Time) ([]schema.Scratch, error) {
	return f.scratches, nil
}
func (f *fakeSource) Execs(context.Context, time.Time) ([]schema.ScratchExec, error) {
	return f.execs, nil
}
func (f *fakeSource) RepoMetrics(context.Context, time.Time) ([]schema.RepoMetrics, error) {
	return f.repoMetrics, nil
}

// openPublished fetches the snapshot database dest holds and opens it.
func openPublished(t *testing.T, dest billy.Filesystem) *sqlite3.Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fetched.db")
	if err := sqlitex.Fetch(dest, Object, path); err != nil {
		t.Fatalf("fetching snapshot database: %v", err)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("opening snapshot database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSnapshotFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 15, 4, 5, 0, time.UTC)
	src := &fakeSource{
		attempts: []schema.RebuildAttempt{
			measuredAttempt("pypi", "pkgA", "1.0", "r1", 120, 10),
			attempt("npm", "pkgN", "2.0", "r2", false, schema.RebuildStatusError, at(0)),
		},
		runs: []schema.Run{
			{ID: "run-1", BenchmarkName: "bench", Created: at(0)},
		},
		sessions: []schema.AgentSession{
			{
				ID:        "s1",
				ScratchID: "sc1",
				Target:    rebuild.Target{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl"},
				Status:    schema.AgentSessionStatusCompleted,
				Usage:     &schema.TokenUsage{Input: 100, Output: 50},
				Created:   at(0),
			},
		},
		iterations: []schema.AgentIteration{
			{ID: "it1", SessionID: "s1", Number: 1, Status: schema.AgentIterationStatusSuccess, Usage: &schema.TokenUsage{Output: 150}},
		},
		scratches: []schema.Scratch{
			{ID: "sc1", MachineClass: schema.MachineClassStandard, State: schema.ScratchDeleted, Created: at(0), Updated: at(10 * time.Minute)},
		},
		execs: []schema.ScratchExec{
			{ID: "e1", ScratchID: "sc1", Cmd: []string{"go", "build"}, State: schema.ScratchExecCompleted,
				CreatedAt: at(0), StartedAt: at(time.Minute), FinishedAt: at(2 * time.Minute), Updated: at(2 * time.Minute)},
		},
		repoMetrics: []schema.RepoMetrics{
			{URI: "https://github.com/example/pkgA", Bytes: 4096, Commits: 12},
		},
	}
	dest := memfs.New()
	res, err := Rollup(ctx, src, dest, Options{Project: "proj-x", ToolVersion: "v-test", Now: now})
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	// The rollup reads everything: one attempts scan, from the zero watermark.
	if len(src.since) != 1 || !src.since[0].IsZero() {
		t.Errorf("rollup attempts scans = %v, want one FullScan", src.since)
	}
	// Result row counts.
	if got := res.RowCounts[TableAttempts]; got != 2 {
		t.Errorf("attempts count = %d, want 2", got)
	}
	if got := res.RowCounts[TableRuns]; got != 1 {
		t.Errorf("runs count = %d, want 1", got)
	}
	db := openPublished(t, dest)
	// Meta round-trips with the scan-start watermark, and the era sits in
	// the file header.
	meta, err := ReadMeta(db)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if !meta.BuiltAt.Equal(now) || !meta.Watermark.Equal(now) {
		t.Errorf("meta times = (%v, %v), want both %v", meta.BuiltAt, meta.Watermark, now)
	}
	if meta.SourceProject != "proj-x" || meta.ToolVersion != "v-test" {
		t.Errorf("meta provenance unexpected: %+v", meta)
	}
	if v, err := sqlitex.Version(db); err != nil || v != SchemaVersion {
		t.Errorf("schema version = (%d, %v), want %d", v, err, SchemaVersion)
	}
	// Every result count matches its stored table.
	for name, want := range res.RowCounts {
		assertCount(t, db, "SELECT count(*) FROM "+name, strconv.Itoa(want))
	}
	// Attempt columns land typed. Entity rows carry their source documents.
	assertCount(t, db, `SELECT count(*) FROM attempts WHERE package='pkgA' AND success=1 AND has_costs=1
		AND cost_builder_seconds=140.0 AND build_seconds=120.0 AND deps_seconds=10.0`, "1")
	assertCount(t, db, "SELECT count(*) FROM attempts WHERE raw IS NOT NULL", "2")
	assertCount(t, db, "SELECT count(*) FROM runs WHERE run_id='run-1' AND benchmark_name='bench'", "1")
}

func TestSnapshotReplacesObject(t *testing.T) {
	ctx := context.Background()
	dest := memfs.New()
	now := time.Date(2026, 7, 14, 15, 4, 5, 0, time.UTC)
	if _, err := Rollup(ctx, &fakeSource{}, dest, Options{Now: now}); err != nil {
		t.Fatalf("first Rollup: %v", err)
	}
	if _, err := Rollup(ctx, &fakeSource{}, dest, Options{Now: now.Add(time.Hour)}); err != nil {
		t.Fatalf("second Rollup: %v", err)
	}
	meta, err := ReadMeta(openPublished(t, dest))
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if !meta.BuiltAt.Equal(now.Add(time.Hour)) {
		t.Errorf("meta built_at = %v, want %v (second snapshot replaces the first)", meta.BuiltAt, now.Add(time.Hour))
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	m := Meta{BuiltAt: at(0), Watermark: at(time.Hour), SourceProject: "proj-x", ToolVersion: "v-test"}
	if err := WriteMeta(db, m); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got, err := ReadMeta(db)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if !got.BuiltAt.Equal(m.BuiltAt) || !got.Watermark.Equal(m.Watermark) ||
		got.SourceProject != m.SourceProject || got.ToolVersion != m.ToolVersion {
		t.Errorf("round trip = %+v, want %+v", got, m)
	}
	// A rewrite replaces the row, and a zero watermark survives as zero.
	if err := WriteMeta(db, Meta{BuiltAt: at(time.Hour)}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if got, err := ReadMeta(db); err != nil || !got.Watermark.IsZero() {
		t.Errorf("rewritten meta = (%+v, %v), want zero watermark", got, err)
	}
	assertCount(t, db, "SELECT count(*) FROM snapshot_meta", "1")
}
