// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundex

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/ncruces/go-sqlite3"
)

// snapshotSource adapts fixtures to snapshot.Source so tests exercise the same
// build pipeline the rollup uses.
type snapshotSource struct {
	attempts   []schema.RebuildAttempt
	runs       []schema.Run
	sessions   []schema.AgentSession
	iterations []schema.AgentIteration
}

func (s *snapshotSource) Attempts(context.Context, time.Time) ([]schema.RebuildAttempt, error) {
	return s.attempts, nil
}
func (s *snapshotSource) Runs(context.Context, time.Time) ([]schema.Run, error) { return s.runs, nil }
func (s *snapshotSource) Sessions(context.Context, time.Time) ([]schema.AgentSession, error) {
	return s.sessions, nil
}
func (s *snapshotSource) Iterations(context.Context, time.Time) ([]schema.AgentIteration, error) {
	return s.iterations, nil
}
func (s *snapshotSource) Scratches(context.Context, time.Time) ([]schema.Scratch, error) {
	return nil, nil
}
func (s *snapshotSource) Execs(context.Context, time.Time) ([]schema.ScratchExec, error) {
	return nil, nil
}
func (s *snapshotSource) RepoMetrics(context.Context, time.Time) ([]schema.RepoMetrics, error) {
	return nil, nil
}

func ts(min int) time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

func attemptFixture(pkg, version, runID string, status schema.RebuildStatus, created time.Time) schema.RebuildAttempt {
	return schema.RebuildAttempt{
		Ecosystem:       "pypi",
		Package:         pkg,
		Version:         version,
		Artifact:        version + ".whl",
		RunID:           runID,
		ExecutorVersion: "exec-" + runID,
		Status:          status,
		Success:         status == schema.RebuildStatusSuccess,
		Created:         created,
		Updated:         created,
	}
}

func newTestReader(t *testing.T, src snapshot.Source) *SQLite {
	t.Helper()
	ctx := context.Background()
	dest := memfs.New()
	if _, err := snapshot.Rollup(ctx, src, dest, snapshot.Options{Now: ts(60)}); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := sqlitex.Fetch(dest, snapshot.Object, path); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewSQLite(NewSingleConn(db))
}

func testSource() *snapshotSource {
	return &snapshotSource{
		attempts: []schema.RebuildAttempt{
			attemptFixture("pkgA", "1.0", "r1", schema.RebuildStatusSuccess, ts(0)),
			attemptFixture("pkgA", "1.0", "r2", schema.RebuildStatusError, ts(10)),
			attemptFixture("pkgA", "1.1", "r3", schema.RebuildStatusRunning, ts(20)),
			attemptFixture("pkgB", "2.0", "r4", schema.RebuildStatusSuccess, ts(5)),
		},
		runs: []schema.Run{
			{ID: "r1", BenchmarkName: "bench", BenchmarkHash: "h1", Created: ts(0)},
			{ID: "r4", BenchmarkName: "other", BenchmarkHash: "h2", Created: ts(5)},
		},
		sessions: []schema.AgentSession{
			{ID: "s1", Target: rebuild.Target{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl"}, Status: schema.AgentSessionStatusCompleted, Created: ts(1), Updated: ts(2)},
			{ID: "s2", Target: rebuild.Target{Ecosystem: "pypi", Package: "pkgB", Version: "2.0", Artifact: "2.0.whl"}, Status: schema.AgentSessionStatusRunning, Created: ts(6), Updated: ts(7)},
		},
		iterations: []schema.AgentIteration{
			{ID: "it2", SessionID: "s1", Number: 2, Created: ts(3), Updated: ts(3)},
			{ID: "it1", SessionID: "s1", Number: 1, Created: ts(2), Updated: ts(2)},
			{ID: "it3", SessionID: "s2", Number: 1, Created: ts(7), Updated: ts(7)},
		},
	}
}

func runIDs(rebuilds []Rebuild) []string {
	var ids []string
	for _, r := range rebuilds {
		ids = append(ids, r.RunID)
	}
	return ids
}

func TestSQLiteRecentPackageRebuilds(t *testing.T) {
	r := newTestReader(t, testSource())
	got, err := r.RecentPackageRebuilds(context.Background(), "pypi", "pkgA")
	if err != nil {
		t.Fatalf("RecentPackageRebuilds: %v", err)
	}
	// Pending attempts filter out, and results order newest first.
	if diff := cmp.Diff([]string{"r2", "r1"}, runIDs(got)); diff != "" {
		t.Errorf("run IDs diff (-want +got):\n%s", diff)
	}
}

func TestSQLiteFetchRebuilds(t *testing.T) {
	r := newTestReader(t, testSource())
	for _, tc := range []struct {
		name string
		req  FetchRebuildRequest
		want []string // run IDs, newest first
	}{
		{
			name: "TargetIncludePending",
			req: FetchRebuildRequest{
				Target: &rebuild.Target{Ecosystem: "pypi", Package: "pkgA"},
				Opts:   FetchRebuildOpts{IncludePending: true},
				Limit:  10,
			},
			want: []string{"r3", "r2", "r1"},
		},
		{
			name: "ByRun",
			req:  FetchRebuildRequest{Runs: []string{"r4"}},
			want: []string{"r4"},
		},
		{
			name: "ByExecutor",
			req:  FetchRebuildRequest{Executors: []string{"exec-r1"}},
			want: []string{"r1"},
		},
		{
			// The SQL LIMIT applies before the client-side pending filter,
			// matching the Firestore reader: the newest attempt (r3) is
			// pending, so a limit of one returns nothing.
			name: "LimitAppliesBeforePendingFilter",
			req:  FetchRebuildRequest{Limit: 1},
			want: nil,
		},
		{
			name: "LatestPerPackage",
			req:  FetchRebuildRequest{LatestPerPackage: true},
			want: []string{"r2", "r4"},
		},
		{
			// A bench request skips the SQL LIMIT, so pkgA's completed
			// attempt is found even though the newest attempt overall is
			// pending.
			name: "BenchSkipsSQLLimit",
			req: FetchRebuildRequest{
				Bench: &benchmark.PackageSet{
					Metadata: benchmark.Metadata{Count: 1},
					Packages: []benchmark.Package{{Ecosystem: "pypi", Name: "pkgA"}},
				},
				Limit: 1,
			},
			want: []string{"r2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.FetchRebuilds(context.Background(), &tc.req)
			if err != nil {
				t.Fatalf("FetchRebuilds: %v", err)
			}
			if diff := cmp.Diff(tc.want, runIDs(got)); diff != "" {
				t.Errorf("run IDs diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSQLiteFetchAttempt(t *testing.T) {
	r := newTestReader(t, testSource())
	target := rebuild.Target{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl"}
	got, err := r.FetchAttempt(context.Background(), target, "r2")
	if err != nil {
		t.Fatalf("FetchAttempt: %v", err)
	}
	if got.RunID != "r2" || got.Status != schema.RebuildStatusError {
		t.Errorf("got %+v", got)
	}
	if _, err := r.FetchAttempt(context.Background(), target, "missing"); err == nil {
		t.Error("expected error for missing attempt")
	}
}

func TestSQLiteLatestTrackedPackages(t *testing.T) {
	r := newTestReader(t, testSource())
	tracked := feed.TrackedPackageIndex{"pypi": {"pkgA": true}}
	got, err := r.LatestTrackedPackages(context.Background(), tracked)
	if err != nil {
		t.Fatalf("LatestTrackedPackages: %v", err)
	}
	// pkgA's newest attempt (r3) is RUNNING and skipped in favor of r2, and
	// pkgB is untracked.
	if diff := cmp.Diff([]string{"r2"}, runIDs(got)); diff != "" {
		t.Errorf("run IDs diff (-want +got):\n%s", diff)
	}
}

func TestSQLiteFetchRuns(t *testing.T) {
	r := newTestReader(t, testSource())
	got, err := r.FetchRuns(context.Background(), FetchRunsOpts{BenchmarkHash: "h1"})
	if err != nil {
		t.Fatalf("FetchRuns: %v", err)
	}
	if len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("got %+v, want [r1]", got)
	}
	byName, err := r.FetchRuns(context.Background(), FetchRunsOpts{IDs: []string{"r1", "r4"}, BenchmarkName: "other"})
	if err != nil {
		t.Fatalf("FetchRuns by name: %v", err)
	}
	if len(byName) != 1 || byName[0].ID != "r4" {
		t.Errorf("by name got %+v, want [r4]", byName)
	}
}

func TestSQLiteFetchSessions(t *testing.T) {
	r := newTestReader(t, testSource())
	got, err := r.FetchSessions(context.Background(), &FetchSessionsReq{
		PartialTarget: &rebuild.Target{Ecosystem: "pypi", Package: "pkgA"},
	})
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Errorf("got %+v, want [s1]", got)
	}
	all, err := r.FetchSessions(context.Background(), &FetchSessionsReq{Limit: 10})
	if err != nil {
		t.Fatalf("FetchSessions all: %v", err)
	}
	// Results sort ascending by creation time.
	if len(all) != 2 || all[0].ID != "s1" || all[1].ID != "s2" {
		t.Errorf("got %+v, want [s1 s2]", all)
	}
	// The range bounds are inclusive: s1 (created ts(1)) falls before the
	// range and s2 (created ts(6)) sits exactly on Until.
	ranged, err := r.FetchSessions(context.Background(), &FetchSessionsReq{Since: ts(2), Until: ts(6)})
	if err != nil {
		t.Fatalf("FetchSessions ranged: %v", err)
	}
	if len(ranged) != 1 || ranged[0].ID != "s2" {
		t.Errorf("ranged got %+v, want [s2]", ranged)
	}
}

func TestSQLiteFetchIterations(t *testing.T) {
	r := newTestReader(t, testSource())
	got, err := r.FetchIterations(context.Background(), &FetchIterationsReq{SessionID: "s1"})
	if err != nil {
		t.Fatalf("FetchIterations: %v", err)
	}
	if len(got) != 2 || got[0].ID != "it1" || got[1].ID != "it2" {
		t.Errorf("got %+v, want [it1 it2] in created order", got)
	}
	if _, err := r.FetchIterations(context.Background(), &FetchIterationsReq{}); err == nil {
		t.Error("expected error for empty session ID")
	}
}
