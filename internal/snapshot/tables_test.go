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
		TablePackageSignals:  docsOf(src.signals),
	}, Meta{BuiltAt: at(time.Hour)})
	if err != nil {
		t.Fatalf("fillSnapshotDB: %v", err)
	}
	return db, counts
}

func TestFillSnapshotDB(t *testing.T) {
	db, counts := fill(t, &fakeSource{
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
	// Derived tables materialize and join on shared keys, with counts
	// reported (2 attempt + 1 session + 1 scratch observations).
	if counts[TableCostObservations] != 4 {
		t.Errorf("cost_observations count = %d, want 4", counts[TableCostObservations])
	}
	assertCount(t, db, `SELECT count(*) FROM attempts a
		JOIN cost_observations o ON a.run_id = o.run_id AND o.source='attempt'`, "2")
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

func TestCostObservations(t *testing.T) {
	db, _ := fill(t, &fakeSource{
		attempts: []schema.RebuildAttempt{
			measuredAttempt("pypi", "pkgA", "1.0", "r1", 120, 10),
			attempt("pypi", "pkgB", "1.0", "r2", false, schema.RebuildStatusRunning, at(0)), // excluded
		},
		sessions: []schema.AgentSession{
			{
				ID:        "s1",
				ScratchID: "sc1",
				Target:    rebuild.Target{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl"},
				Usage:     &schema.TokenUsage{Input: 100, Output: 50, Model: "gemini-2.5-pro"},
				Created:   at(0),
			},
		},
		scratches: []schema.Scratch{
			{ID: "sc1", MachineClass: schema.MachineClassStandard, State: schema.ScratchDeleted, Created: at(0), Updated: at(10 * time.Minute)},
			{ID: "sc2", MachineClass: schema.MachineClassStandard, State: schema.ScratchReady, Created: at(0)}, // still live → skipped
		},
	})
	// Phase timings come from BuildTimings. Builder/storage measures from Costs.
	assertCount(t, db, `SELECT count(*) FROM cost_observations WHERE source='attempt'
		AND build_seconds=120.0 AND deps_seconds=10.0 AND builder_seconds=140.0
		AND builder_pool='SHRIMP' AND artifact_bytes=1048576`, "1")
	assertCount(t, db, "SELECT count(*) FROM cost_observations WHERE source='attempt'", "1")
	assertCount(t, db, "SELECT count(*) FROM cost_observations WHERE source='agent_session' AND input_tokens=100 AND model='gemini-2.5-pro'", "1")
	// Scratch cost attributed to the linked session's target. Live VM skipped.
	assertCount(t, db, `SELECT count(*) FROM cost_observations WHERE source='scratch_vm'
		AND package='pkgA' AND session_id='s1' AND vm_seconds=600.0`, "1")
	assertCount(t, db, "SELECT count(*) FROM cost_observations WHERE source='scratch_vm'", "1")
}

func TestEcosystemDaily(t *testing.T) {
	day1 := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)
	costed := attempt("pypi", "p1", "1.0", "r1", true, schema.RebuildStatusSuccess, day1)
	costed.Costs = &schema.AttemptCosts{BuilderSeconds: 120}
	db, _ := fill(t, &fakeSource{
		attempts: []schema.RebuildAttempt{
			// Day 1, pypi: two attempts, one first-time success, one error.
			costed,
			attempt("pypi", "p2", "1.0", "r2", false, schema.RebuildStatusError, day1),
			// Day 2, pypi: p1 succeeds again (NOT a first-time success).
			attempt("pypi", "p1", "1.1", "r3", true, schema.RebuildStatusSuccess, day2),
			// Running attempt ignored.
			attempt("pypi", "p3", "1.0", "r4", false, schema.RebuildStatusRunning, day1),
			// No created stamp: aggregates under the empty day rather than
			// vanishing on NULL join keys.
			attempt("pypi", "p4", "1.0", "r5", false, schema.RebuildStatusFailure, time.Time{}),
		},
		sessions: []schema.AgentSession{
			{
				ID:        "s1",
				ScratchID: "sc1",
				Target:    rebuild.Target{Ecosystem: "pypi", Package: "p1"},
				Usage:     &schema.TokenUsage{Input: 600, Output: 400},
				Created:   day1,
			},
		},
		scratches: []schema.Scratch{
			{ID: "sc1", State: schema.ScratchDeleted, Created: day1, Updated: day1.Add(time.Hour)},
		},
	})
	assertCount(t, db, `SELECT count(*) FROM ecosystem_daily WHERE ecosystem='pypi' AND day='2026-07-01'
		AND attempts=2 AND successes=1 AND errors=1 AND distinct_pkgs_attempted=2
		AND first_time_successes=1 AND tokens=1000 AND builder_seconds=120.0 AND scratch_vm_seconds=3600.0`, "1")
	assertCount(t, db, "SELECT count(*) FROM ecosystem_daily WHERE ecosystem='pypi' AND day='' AND attempts=1 AND successes=0", "1")
	assertCount(t, db, `SELECT count(*) FROM ecosystem_daily WHERE ecosystem='pypi' AND day='2026-07-02'
		AND first_time_successes=0 AND successes=1`, "1")
}
