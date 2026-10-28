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
)

func coverageFixture(t *testing.T) memAnalytics {
	return newMemAnalytics(t,
		`CREATE TABLE coverage_weekly (ecosystem TEXT, week TEXT, packages_attempted INT, packages_current INT, packages_stale INT, coverage REAL, weighted_coverage REAL, newly_broken INT)`,
		`INSERT INTO coverage_weekly VALUES ('npm', '2026-08-24', 10, 6, 2, 0.6, 0.75, 1), ('npm', '2026-08-31', 12, 9, 1, 0.75, 0.9, 0), ('maven', '2026-08-31', 4, 4, 0, 1.0, NULL, 0)`,
		`CREATE TABLE signal_coverage (ecosystem TEXT, universe_packages INT, universe_mass REAL, tracked_packages INT, tracked_mass REAL, current_packages INT, current_mass REAL, covered_mass_share REAL, sidecar_built_at TEXT)`,
		`INSERT INTO signal_coverage VALUES ('npm', 300, 100.0, 12, 3.0, 9, 2.5, 0.025, ''), ('pypi', 4000, 100.0, 0, 0.0, 0, 0.0, 0.0, '')`,
		`CREATE TABLE campaigns (ecosystem TEXT, package TEXT, version TEXT, artifact TEXT, state TEXT, stage TEXT)`,
		`INSERT INTO campaigns VALUES ('npm', 'a', '1', 'a.tgz', 'DONE', 'replay'), ('npm', 'b', '1', 'b.tgz', 'DONE', 'agent'), ('npm', 'c', '1', 'c.tgz', 'QUEUED', 'infer'), ('npm', 'd', '1', 'd.tgz', 'NEEDS_TRIAGE', 'agent'), ('npm', 'e', '1', 'e.tgz', 'INFLIGHT', 'replay')`,
		`CREATE TABLE package_stats (ecosystem TEXT, package TEXT, ever_built INT)`,
		`INSERT INTO package_stats VALUES ('npm', 'a', 1), ('npm', 'b', 0), ('npm', 'c', 1)`,
		// a's later version does not move its first reproduction.
		`CREATE TABLE version_stats (ecosystem TEXT, package TEXT, version TEXT, first_succeeded_at TEXT)`,
		`INSERT INTO version_stats VALUES ('npm', 'a', '1', '2026-08-30T10:15:00.000Z'), ('npm', 'a', '2', '2026-08-30T12:00:00.000Z'), ('npm', 'b', '1', NULL), ('npm', 'c', '1', '2026-08-30T11:30:00.000Z')`,
		`CREATE TABLE ecosystem_daily (ecosystem TEXT, day TEXT, attempts INT, successes INT, tokens INT, builder_seconds REAL, scratch_vm_seconds REAL)`,
		`INSERT INTO ecosystem_daily VALUES ('npm', '2026-08-30', 3, 1, 100, 3600, 1800), ('maven', '2026-08-30', 1, 1, 0, 3600, 0), ('npm', '2026-08-31', 2, 2, 0, 0, 0), ('npm', '', 5, 0, 0, 0, 0)`,
	)
}

// coverageOverview is the band coverageFixture yields.
var coverageOverview = &Overview{
	AsOf: "2026-09-01 00:00:00 UTC", InScope: 4300, Started: 12, Reproduced: 9,
	Ecos: []EcoBand{
		{Ecosystem: "npm", InScope: 300, Started: 12, Reproduced: 9, EverBuilt: 2, Growth: [][2]int64{
			{time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC).Unix(), 1},
			{time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC).Unix(), 2},
		}},
		{Ecosystem: "pypi", InScope: 4000},
	},
	Activity: Activity{Start: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC).Unix(), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), Days: []DayStat{
		{Day: "2026-08-30", Attempts: 4, Succeeded: 2, Failed: 2, Tokens: 100, BuilderHours: 2, VMHours: 0.5},
		{Day: "2026-08-31", Attempts: 2, Succeeded: 2},
	}},
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
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}, Overview: coverageOverview},
		},
		{
			name: "EcosystemTab",
			req:  CoverageRequest{Eco: "npm"},
			want: func() CoverageData {
				detail := npm
				detail.Weeks = npmWeeks
				return CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Eco: "npm", Ecos: []CoverageEco{maven, detail, pypi}, Detail: &detail, Overview: coverageOverview}
			}(),
		},
		{
			name: "UnknownTabFallsBack",
			req:  CoverageRequest{Eco: "cratesio"},
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}, Overview: coverageOverview},
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
