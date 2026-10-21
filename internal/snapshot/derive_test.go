// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
)

// TestDeltaRefreshesDerived applies a segment that reaches every scoped
// table through every path (a build on a new version, an agent win, a
// session's tokens, a scratch's deletion, a campaign's state) and checks
// that each derived table equals a whole re-derivation of the same rows.
func TestDeltaRefreshesDerived(t *testing.T) {
	day2 := base.AddDate(0, 0, 1)
	db, _ := fill(t, &fakeSource{
		attempts: []schema.RebuildAttempt{
			attempt("pypi", "pkgA", "1.0", "r1", true, schema.RebuildStatusSuccess, at(0)),
			attempt("pypi", "pkgA", "2.0", "r2", false, schema.RebuildStatusFailure, at(time.Hour)),
			attempt("pypi", "pkgB", "1.0", "r3", false, schema.RebuildStatusError, at(0)),
			attempt("npm", "pkgN", "1.0", "r4", true, schema.RebuildStatusSuccess, at(0)),
		},
		sessions: []schema.AgentSession{
			agentSession("s1", "r3", "pypi", "pkgB", "1.0", at(time.Hour)),
			{ID: "s2", ScratchID: "sc2", Target: rebuild.Target{Ecosystem: "npm", Package: "pkgN", Version: "1.0", Artifact: "1.0.whl"}, Created: at(0)},
		},
		iterations: []schema.AgentIteration{agentIteration("s1", 1, schema.AgentIterationStatusFailed, at(time.Hour))},
		scratches: []schema.Scratch{
			{ID: "sc1", State: schema.ScratchReady, Created: at(0), Updated: at(0)},
			{ID: "sc2", State: schema.ScratchDeleted, Created: at(0), Updated: at(time.Hour)},
		},
		campaigns: []scheduler.Campaign{
			{Ecosystem: "pypi", Package: "pkgA", Version: "2.0", Artifact: "2.0.whl", State: scheduler.StateQueued, Published: at(-24 * time.Hour), Updated: at(0)},
			{Ecosystem: "pypi", Package: "pkgC", Version: "1.0", Artifact: "1.0.whl", State: scheduler.StateQueued, Published: at(-24 * time.Hour), Updated: at(0)},
		},
		signals: []signals.PackageSignal{
			{Ecosystem: "pypi", Package: "pkgA", Score: 0.9}, {Ecosystem: "pypi", Package: "pkgB", Score: 0.5},
			{Ecosystem: "pypi", Package: "pkgC", Score: 0.3}, {Ecosystem: "npm", Package: "pkgN", Score: 0.7},
		},
		universe: []SignalUniverse{{Ecosystem: "pypi", Packages: 3, ScoreMass: 1.7}, {Ecosystem: "npm", Packages: 1, ScoreMass: 0.7}},
	})
	s1 := agentSession("s1", "r3", "pypi", "pkgB", "1.0", at(time.Hour))
	s1.ScratchID, s1.Usage, s1.Updated = "sc1", &schema.TokenUsage{Input: 10, Output: 5}, day2
	dest := memfs.New()
	name, err := docdb.WriteSegment(dest, DeltaPrefix, day2, Tables(), map[string][]json.RawMessage{
		TableAttempts: docsOf([]schema.RebuildAttempt{
			// pkgA 10.0 becomes the latest version and fails on day 2.
			attempt("pypi", "pkgA", "10.0", "r5", false, schema.RebuildStatusFailure, day2),
			// pkgC's first build succeeds on day 2.
			attempt("pypi", "pkgC", "1.0", "r6", true, schema.RebuildStatusSuccess, day2),
			// A replay of r1 changes nothing.
			attempt("pypi", "pkgA", "1.0", "r1", true, schema.RebuildStatusSuccess, at(0)),
		}),
		// s1 gains tokens and a scratch, and the agent confirms pkgB.
		TableAgentSessions:   docsOf([]schema.AgentSession{s1}),
		TableAgentIterations: docsOf([]schema.AgentIteration{agentIteration("s1", 2, schema.AgentIterationStatusSuccess, day2)}),
		// sc1 is deleted, costing its session's target.
		TableScratchVMs: docsOf([]schema.Scratch{{ID: "sc1", State: schema.ScratchDeleted, Created: at(0), Updated: day2}}),
		// pkgA 2.0 leaves the queue.
		TableCampaigns: docsOf([]scheduler.Campaign{
			{Ecosystem: "pypi", Package: "pkgA", Version: "2.0", Artifact: "2.0.whl", State: scheduler.StateDone, Published: at(-24 * time.Hour), Updated: day2},
		}),
	})
	if err != nil {
		t.Fatalf("WriteSegment: %v", err)
	}
	if err := docdb.ApplySegment(db, dest, Tables(), name); err != nil {
		t.Fatalf("ApplySegment: %v", err)
	}
	// Spot checks on the paths, then the whole comparison.
	assertCount(t, db, "SELECT vrank FROM version_stats WHERE package='pkgA' AND version='10.0'", "1")
	assertCount(t, db, "SELECT vrank FROM version_stats WHERE package='pkgA' AND version='2.0'", "2")
	assertCount(t, db, "SELECT count(*) FROM version_stats WHERE package='pkgB' AND current=1", "1")
	assertCount(t, db, "SELECT first_time_successes FROM ecosystem_daily WHERE ecosystem='pypi' AND day='2026-07-02'", "2")
	assertCount(t, db, "SELECT tokens FROM ecosystem_daily WHERE ecosystem='pypi' AND day='2026-07-01'", "15")
	assertCount(t, db, "SELECT count(*) FROM cost_observations WHERE source='scratch_vm' AND scratch_id='sc1' AND package='pkgB'", "1")
	assertCount(t, db, "SELECT count(*) FROM top_gaps WHERE package='pkgA' AND latest_version='10.0'", "1")
	assertCount(t, db, "SELECT count(*) FROM temp.sqlite_schema", "0")
	assertDerivedWhole(t, db)
}

// assertDerivedWhole re-derives every derived table a delta refreshes
// whole beside the current one and fails on any row either has alone.
func assertDerivedWhole(t *testing.T, db *sqlite3.Conn) {
	t.Helper()
	refreshed := func(td docdb.TableDef) bool { return td.Query != "" && td.Scope != docdb.Unrefreshed }
	for _, td := range Tables() {
		if !refreshed(td) {
			continue
		}
		if err := db.Exec("ALTER TABLE " + td.Name + " RENAME TO inc_" + td.Name); err != nil {
			t.Fatal(err)
		}
		for _, idx := range td.Indexes {
			if err := db.Exec("DROP INDEX idx_" + td.Name + "_" + strings.Join(idx, "_")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := refreshDerived(db); err != nil {
		t.Fatalf("refreshDerived: %v", err)
	}
	for _, td := range Tables() {
		if !refreshed(td) {
			continue
		}
		for _, pair := range [][2]string{{td.Name, "inc_" + td.Name}, {"inc_" + td.Name, td.Name}} {
			if got := queryText(t, db, "SELECT count(*) FROM (SELECT * FROM "+pair[0]+" EXCEPT SELECT * FROM "+pair[1]+")"); got != "0" {
				t.Errorf("%s: %s rows only in %s, the first for %s", td.Name, got, pair[0],
					queryText(t, db, "SELECT ecosystem FROM (SELECT * FROM "+pair[0]+" EXCEPT SELECT * FROM "+pair[1]+")"))
			}
		}
	}
}

// TestDeltaOnOlderBaseRollsBack applies a segment to a base whose
// version_stats predates vrank. The scoped refresh cannot write its rows,
// so the apply fails and the copy keeps every row it had, documents
// included, until a base built by this registry replaces it.
func TestDeltaOnOlderBaseRollsBack(t *testing.T) {
	db, _ := fill(t, &fakeSource{attempts: []schema.RebuildAttempt{
		attempt("pypi", "pkgA", "1.0", "r1", true, schema.RebuildStatusSuccess, at(0)),
	}})
	if err := db.Exec("ALTER TABLE version_stats DROP COLUMN vrank"); err != nil {
		t.Fatalf("dropping vrank: %v", err)
	}
	dest := memfs.New()
	name, err := docdb.WriteSegment(dest, DeltaPrefix, at(time.Hour), Tables(), map[string][]json.RawMessage{
		TableAttempts: docsOf([]schema.RebuildAttempt{attempt("pypi", "pkgA", "2.0", "r2", false, schema.RebuildStatusFailure, at(time.Hour))}),
	})
	if err != nil {
		t.Fatalf("WriteSegment: %v", err)
	}
	if err := docdb.ApplySegment(db, dest, Tables(), name); err == nil {
		t.Fatal("expected the apply to fail on the older base")
	}
	assertCount(t, db, "SELECT count(*) FROM attempts", "1")
	assertCount(t, db, "SELECT count(*) FROM version_stats", "1")
	assertCount(t, db, "SELECT count(*) FROM package_stats WHERE attempt_count = 1", "1")
	assertCount(t, db, "SELECT count(*) FROM temp.sqlite_schema", "0")
}
