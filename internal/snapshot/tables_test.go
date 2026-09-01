// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/ncruces/go-sqlite3"
)

var base = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func at(offset time.Duration) time.Time { return base.Add(offset) }

func attempt(eco, pkg, ver, runID string, success bool, status schema.RebuildStatus, created time.Time) schema.RebuildAttempt {
	return schema.RebuildAttempt{
		Ecosystem: eco,
		Package:   pkg,
		Version:   ver,
		Artifact:  ver + ".whl",
		RunID:     runID,
		Success:   success,
		Status:    status,
		Created:   created,
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }

// measuredAttempt builds a successful attempt carrying build phase timings
// and measured Costs.
func measuredAttempt(eco, pkg, ver, runID string, buildSec, depsSec float64) schema.RebuildAttempt {
	a := attempt(eco, pkg, ver, runID, true, schema.RebuildStatusSuccess, at(0))
	a.BuildTimings = &rebuild.BuildTimings{
		Deps:  durPtr(time.Duration(depsSec * float64(time.Second))),
		Build: durPtr(time.Duration(buildSec * float64(time.Second))),
	}
	a.Costs = &schema.AttemptCosts{
		BuilderSeconds: buildSec + 20,
		BuilderPool:    schema.ShrimpSize,
		ArtifactBytes:  1 << 20,
	}
	return a
}

// queryText runs a single-row, single-column query and returns the value's
// text form.
func queryText(t *testing.T, db *sqlite3.Conn, sql string) string {
	t.Helper()
	stmt, _, err := db.Prepare(sql)
	if err != nil {
		t.Fatalf("prepare %q: %v", sql, err)
	}
	defer stmt.Close()
	if !stmt.Step() {
		t.Fatalf("no rows for %q: %v", sql, stmt.Err())
	}
	return stmt.ColumnText(0)
}

// assertCount asserts a counting query returns want.
func assertCount(t *testing.T, db *sqlite3.Conn, sql string, want string) {
	t.Helper()
	if got := queryText(t, db, sql); got != want {
		t.Errorf("%s = %s, want %s", sql, got, want)
	}
}

// fill builds an in-memory snapshot database from source fixtures.
func fill(t *testing.T, src *fakeSource) (*sqlite3.Conn, map[string]int) {
	t.Helper()
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	counts, err := fillSnapshotDB(db, map[string][]json.RawMessage{
		TableAttempts:        docsOf(src.attempts),
		TableRuns:            docsOf(src.runs),
		TableAgentSessions:   docsOf(src.sessions),
		TableAgentIterations: docsOf(src.iterations),
		TableScratchVMs:      docsOf(src.scratches),
		TableScratchExecs:    docsOf(src.execs),
		TableRepoMetrics:     docsOf(src.repoMetrics),
	}, Meta{BuiltAt: at(time.Hour)})
	if err != nil {
		t.Fatalf("fillSnapshotDB: %v", err)
	}
	return db, counts
}

func TestFillSnapshotDB(t *testing.T) {
	db, _ := fill(t, &fakeSource{
		attempts: []schema.RebuildAttempt{
			measuredAttempt("pypi", "pkgA", "1.0", "r1", 120, 10),
			attempt("npm", "pkgN", "2.0", "r2", false, schema.RebuildStatusError, at(0)),
		},
		runs: []schema.Run{{ID: "run-1", BenchmarkName: "bench", Created: at(0)}},
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
			{ID: "e2", ScratchID: "sc1", State: schema.ScratchExecLost, CreatedAt: at(0), FinishedAt: at(time.Minute), Updated: at(time.Minute)},
		},
		repoMetrics: []schema.RepoMetrics{
			{URI: "https://github.com/example/pkgA", Bytes: 4096, Commits: 12},
		},
	})
	assertCount(t, db, "SELECT count(*) FROM attempts", "2")
	assertCount(t, db, "SELECT count(*) FROM runs WHERE run_id='run-1' AND benchmark_name='bench'", "1")
	// Cost columns and phase timings extract typed and queryable.
	assertCount(t, db, `SELECT count(*) FROM attempts WHERE package='pkgA' AND has_costs=1
		AND cost_builder_seconds=140.0 AND cost_builder_pool='SHRIMP'
		AND build_seconds=120.0 AND deps_seconds=10.0`, "1")
	// Unset time columns are NULL, set ones use the uniform UTC encoding.
	assertCount(t, db, "SELECT count(*) FROM attempts WHERE started IS NULL", "2")
	assertCount(t, db, "SELECT count(*) FROM attempts WHERE created='2026-07-01T12:00:00.000Z'", "2")
	// Session target and token columns extract from the document.
	assertCount(t, db, `SELECT count(*) FROM agent_sessions WHERE session_id='s1'
		AND ecosystem='pypi' AND package='pkgA' AND input_tokens=100`, "1")
	// Scratch lifetime derives in SQL.
	assertCount(t, db, "SELECT count(*) FROM scratch_vms WHERE scratch_id='sc1' AND vm_seconds=600.0", "1")
	assertCount(t, db, `SELECT count(*) FROM repo_metrics
		WHERE uri='https://github.com/example/pkgA' AND bytes=4096 AND commits=12`, "1")
	// The exec span derives from the worker clocks. A lost exec never
	// started, so its span is unmeasured rather than zero.
	assertCount(t, db, "SELECT count(*) FROM scratch_execs WHERE exec_id='e1' AND scratch_id='sc1' AND exec_seconds=60.0", "1")
	assertCount(t, db, "SELECT count(*) FROM scratch_execs WHERE exec_id='e2' AND state='lost' AND exec_seconds IS NULL", "1")
	// The stored document round-trips exactly.
	a := measuredAttempt("pypi", "pkgA", "1.0", "r1", 120, 10)
	var got schema.RebuildAttempt
	if err := json.Unmarshal([]byte(queryText(t, db, "SELECT raw FROM attempts WHERE run_id='r1'")), &got); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if diff := cmp.Diff(a, got); diff != "" {
		t.Errorf("raw round-trip diff (-want +got):\n%s", diff)
	}
	// Declared indexes exist (WITHOUT ROWID primary keys add no autoindexes).
	var wantIdx int
	for _, td := range Tables() {
		wantIdx += len(td.Indexes)
	}
	assertCount(t, db, "SELECT count(*) FROM sqlite_master WHERE type='index'", strconv.Itoa(wantIdx))
}
