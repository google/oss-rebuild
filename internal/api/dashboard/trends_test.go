// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
)

// memAnalytics serves one in-memory snapshot database.
type memAnalytics struct{ db *sqlite3.Conn }

func (m memAnalytics) Query(f func(*sqlite3.Conn) error) error { return f(m.db) }
func (m memAnalytics) Freshness() time.Time                    { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }

func newMemAnalytics(t *testing.T, stmts ...string) memAnalytics {
	t.Helper()
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range stmts {
		if err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return memAnalytics{db}
}

func coverageFixture(t *testing.T) memAnalytics {
	return newMemAnalytics(t,
		`CREATE TABLE coverage_weekly (ecosystem TEXT, week TEXT, packages_attempted INT, packages_current INT, packages_stale INT, coverage REAL, weighted_coverage REAL, newly_broken INT)`,
		`INSERT INTO coverage_weekly VALUES ('npm', '2026-08-24', 10, 6, 2, 0.6, 0.75, 1), ('npm', '2026-08-31', 12, 9, 1, 0.75, 0.9, 0), ('maven', '2026-08-31', 4, 4, 0, 1.0, NULL, 0)`,
		`CREATE TABLE signal_coverage (ecosystem TEXT, universe_packages INT, universe_mass REAL, tracked_packages INT, tracked_mass REAL, current_packages INT, current_mass REAL, covered_mass_share REAL, sidecar_built_at TEXT)`,
		`INSERT INTO signal_coverage VALUES ('npm', 300, 100.0, 12, 3.0, 9, 2.5, 0.025, ''), ('pypi', 4000, 100.0, 0, 0.0, 0, 0.0, 0.0, '')`,
		`CREATE TABLE campaigns (ecosystem TEXT, package TEXT, version TEXT, artifact TEXT, state TEXT, stage TEXT)`,
		`INSERT INTO campaigns VALUES ('npm', 'a', '1', 'a.tgz', 'DONE', 'replay'), ('npm', 'b', '1', 'b.tgz', 'DONE', 'agent'), ('npm', 'c', '1', 'c.tgz', 'QUEUED', 'infer'), ('npm', 'd', '1', 'd.tgz', 'NEEDS_TRIAGE', 'agent'), ('npm', 'e', '1', 'e.tgz', 'INFLIGHT', 'replay')`,
	)
}

func TestCoveragePage(t *testing.T) {
	npm := CoverageEco{
		Ecosystem: "npm", HasScope: true, InScope: 300, Started: 12, Reproduced: 9, CoveragePct: 3, WeightedPct: 2,
		HasQueue: true, Queued: 1, InFlight: 1, Done: 2, NeedsTriage: 1,
		DoneByStage: []StageDone{{scheduler.StageReplay, 1}, {scheduler.StageInfer, 0}, {scheduler.StageAgent, 1}},
	}
	npmWeeks := []CoverageWeek{
		{Week: "2026-08-31", Attempted: 12, Reproduced: 9, Behind: 1, SharePct: 75, WeightedPct: 90, HasWeighted: true},
		{Week: "2026-08-24", Attempted: 10, Reproduced: 6, Behind: 2, SharePct: 60, WeightedPct: 75, HasWeighted: true, NewlyBroken: 1},
	}
	maven := CoverageEco{Ecosystem: "maven"}
	pypi := CoverageEco{Ecosystem: "pypi", HasScope: true, InScope: 4000}
	for _, tc := range []struct {
		name string
		req  CoverageRequest
		want CoverageData
	}{
		{
			name: "Comparison",
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}},
		},
		{
			name: "EcosystemTab",
			req:  CoverageRequest{Eco: "npm"},
			want: func() CoverageData {
				detail := npm
				detail.Weeks = npmWeeks
				return CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Eco: "npm", Ecos: []CoverageEco{maven, detail, pypi}, Detail: &detail}
			}(),
		},
		{
			name: "UnknownTabFallsBack",
			req:  CoverageRequest{Eco: "cratesio"},
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoveragePage(context.Background(), tc.req, &Deps{Analytics: coverageFixture(t)})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(&tc.want, got); diff != "" {
				t.Errorf("CoveragePage diff (-want +got):\n%s", diff)
			}
			if err := CoverageTmpl.Execute(io.Discard, got); err != nil {
				t.Errorf("rendering: %v", err)
			}
		})
	}
}

func TestCoveragePageNoAnalytics(t *testing.T) {
	got, err := CoveragePage(context.Background(), CoverageRequest{}, &Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(&CoverageData{}, got); diff != "" {
		t.Errorf("CoveragePage diff (-want +got):\n%s", diff)
	}
}
