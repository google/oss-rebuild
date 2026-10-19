// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

func coverageFixture(t *testing.T) memAnalytics {
	return newMemAnalytics(t,
		`CREATE TABLE coverage_weekly (ecosystem TEXT, week TEXT, packages_attempted INT, packages_current INT, packages_stale INT, coverage REAL, weighted_coverage REAL, newly_broken INT)`,
		`INSERT INTO coverage_weekly VALUES ('npm', '2026-08-24', 10, 6, 2, 0.6, 0.75, 1), ('npm', '2026-08-31', 12, 9, 1, 0.75, 0.9, 0), ('maven', '2026-08-31', 4, 4, 0, 1.0, NULL, 0)`,
		`CREATE TABLE signal_coverage (ecosystem TEXT, universe_packages INT, universe_mass REAL, tracked_packages INT, tracked_mass REAL, attempted_packages INT, attempted_mass REAL, current_packages INT, current_mass REAL, retrying_packages INT, in_flight_packages INT, queued_packages INT, covered_mass_share REAL, sidecar_built_at TEXT)`,
		`INSERT INTO signal_coverage VALUES ('npm', 300, 100.0, 13, 3.0, 11, 2.8, 9, 2.5, 1, 1, 1, 0.025, ''), ('pypi', 4000, 100.0, 0, 0.0, 0, 0.0, 0, 0.0, 0, 0, 0, 0.0, '')`,
		`CREATE TABLE snapshot_meta (built_at TEXT, watermark TEXT, source_project TEXT, tool_version TEXT)`,
		`INSERT INTO snapshot_meta VALUES ('2026-08-31T23:02:00.000Z', '2026-08-31T23:00:00.000Z', '', '')`,
		`CREATE TABLE campaigns (ecosystem TEXT, package TEXT, version TEXT, artifact TEXT, state TEXT, stage TEXT, last_session TEXT)`,
		`INSERT INTO campaigns VALUES ('npm', 'a', '1', 'a.tgz', 'DONE', 'replay', ''), ('npm', 'b', '1', 'b.tgz', 'DONE', 'agent', 's-llm'), ('npm', 'c', '1', 'c.tgz', 'QUEUED', 'infer', ''), ('npm', 'd', '1', 'd.tgz', 'NEEDS_TRIAGE', 'agent', ''), ('npm', 'e', '1', 'e.tgz', 'INFLIGHT', 'replay', ''), ('npm', 'f', '1', 'f.tgz', 'DONE', 'agent', 's-inf'), ('npm', 'g', '1', 'g.tgz', 'DONE', 'agent', 's-seed')`,
		// s-llm and s-inf predate WonBy and won at their first iteration, so
		// the strategy that won tells them apart. s-seed records that a
		// sibling version's seed won it, though the seed is a flow strategy.
		`CREATE TABLE agent_sessions (session_id TEXT, won_by TEXT)`,
		`INSERT INTO agent_sessions VALUES ('s-llm', ''), ('s-inf', ''), ('s-seed', 'seed')`,
		`CREATE TABLE agent_iterations (session_id TEXT, number INT, status TEXT, raw TEXT)`,
		`INSERT INTO agent_iterations VALUES ('s-llm', 1, 'SUCCESS', '{"Strategy": {"flow": {}}}'), ('s-inf', 1, 'SUCCESS', '{"Strategy": {"npm_pack_build": {}}}'), ('s-seed', 1, 'SUCCESS', '{"Strategy": {"flow": {}}}')`,
		`CREATE TABLE package_stats (ecosystem TEXT, package TEXT, ever_built INT)`,
		`INSERT INTO package_stats VALUES ('npm', 'a', 1), ('npm', 'b', 0), ('npm', 'c', 1)`,
		// a's later version does not move its first reproduction.
		`CREATE TABLE version_stats (ecosystem TEXT, package TEXT, version TEXT, first_succeeded_at TEXT)`,
		`INSERT INTO version_stats VALUES ('npm', 'a', '1', '2026-08-30T10:15:00.000Z'), ('npm', 'a', '2', '2026-08-30T12:00:00.000Z'), ('npm', 'b', '1', NULL), ('npm', 'c', '1', '2026-08-30T11:30:00.000Z')`,
		`CREATE TABLE ecosystem_daily (ecosystem TEXT, day TEXT, attempts INT, successes INT, tokens INT, builder_seconds REAL, scratch_vm_seconds REAL,
			inference_seconds REAL, input_tokens INT, cached_input_tokens INT, output_tokens INT, first_version_successes INT)`,
		`INSERT INTO ecosystem_daily VALUES ('npm', '2026-08-30', 3, 1, 100, 3600, 1800, 0, 0, 0, 0, 2), ('maven', '2026-08-30', 1, 1, 0, 3600, 0, 0, 0, 0, 0, 1),
			('npm', '2026-08-31', 2, 2, 0, 0, 0, 0, 1000000, 0, 0, 1), ('npm', '', 5, 0, 0, 0, 0, 0, 0, 0, 0, 0)`,
		`CREATE TABLE popularity_tiers (ecosystem TEXT, tier INT, packages INT, reproduced INT, not_reproduced INT, retrying INT, in_flight INT, queued INT)`,
		`INSERT INTO popularity_tiers VALUES ('npm', 100, 100, 90, 4, 6, 0, 0), ('npm', 1000, 300, 250, 20, 30, 0, 0), ('pypi', 100, 40, 20, 0, 12, 1, 7)`,
		// 2012 folds into the first charted year.
		`CREATE TABLE release_cohorts (ecosystem TEXT, release_year TEXT, reproduced INT, not_reproduced INT, retrying INT, in_flight INT, queued INT)`,
		`INSERT INTO release_cohorts VALUES ('npm', '2012', 1, 0, 0, 0, 0), ('npm', '2014', 2, 1, 0, 0, 0), ('npm', '2016', 5, 1, 1, 0, 2), ('pypi', '2016', 3, 0, 0, 1, 0)`,
	)
}

// The cohorts coverageFixture yields.
var (
	npmTiers      = EcoTiers{Ecosystem: "npm", Tiers: []TierBand{{Ecosystem: "npm", Tier: 100, Packages: 100, Reproduced: 90, NotReproduced: 4, Retrying: 6}, {Ecosystem: "npm", Tier: 1000, Packages: 300, Reproduced: 250, NotReproduced: 20, Retrying: 30}}}
	pypiTiers     = EcoTiers{Ecosystem: "pypi", Tiers: []TierBand{{Ecosystem: "pypi", Tier: 100, Packages: 40, Reproduced: 20, Retrying: 12, InFlight: 1, Queued: 7}}}
	releaseYears  = []string{"\u22642014", "2015", "2016"}
	npmReleases   = EcoReleases{Ecosystem: "npm", Counts: [][5]int64{{3, 1, 0, 0, 0}, {}, {5, 1, 1, 0, 2}}}
	pypiReleases  = EcoReleases{Ecosystem: "pypi", Counts: [][5]int64{{}, {}, {3, 0, 0, 1, 0}}}
	mavenSpend    = EcoSpend{Ecosystem: "maven", Builder: 3600 * builderCoreHPerSecond, NewVersions: 1, Share: 31}
	npmSpend      = EcoSpend{Ecosystem: "npm", Builder: 3600 * builderCoreHPerSecond, Guests: 1800 * guestCoreHPerSecond, LLM: 1e6 * inputCoreHPerToken, NewVersions: 3, Share: 69}
	coverageSpend = func(ecos ...EcoSpend) *Spend { return &Spend{From: "2026-08-25", To: "2026-08-31", Ecos: ecos} }
)

// coverageOverview is the band coverageFixture yields.
var coverageOverview = &Overview{
	AsOf:  "2026-08-31 23:00:00 UTC",
	Total: EcoBand{Ecosystem: "All ecosystems", InScope: 13, Attempted: 11, Reproduced: 9, Retrying: 1, InFlight: 1, Queued: 1},
	Ecos: []EcoBand{
		{Ecosystem: "npm", InScope: 13, Attempted: 11, Reproduced: 9, Retrying: 1, InFlight: 1, Queued: 1, EverBuilt: 2, Growth: [][2]int64{
			{time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC).Unix(), 1},
			{time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC).Unix(), 2},
		}},
		{Ecosystem: "pypi"},
	},
	Activity: Activity{Start: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC).Unix(), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), Days: []DayStat{
		{Day: "2026-08-30", Attempts: 4, Succeeded: 2, Failed: 2, Tokens: 100, BuilderHours: 2, VMHours: 0.5},
		{Day: "2026-08-31", Attempts: 2, Succeeded: 2},
	}},
}

func TestCoveragePage(t *testing.T) {
	npm := CoverageEco{
		Ecosystem: "npm", HasScope: true, InScope: 13, Attempted: 11, Reproduced: 9, SuccessPct: 81, WeightedPct: 89,
		HasQueue: true, Queued: 1, InFlight: 1, Done: 4, NeedsTriage: 1,
		DoneByStage: []StageDone{{Stage: scheduler.StageReplay, Done: 1}, {Stage: scheduler.StageInfer}, {Stage: scheduler.StageAgent, Done: 3, LLMDone: 1}},
	}
	maven := CoverageEco{Ecosystem: "maven"}
	pypi := CoverageEco{Ecosystem: "pypi", HasScope: true}
	for _, tc := range []struct {
		name string
		req  CoverageRequest
		want CoverageData
	}{
		{
			name: "Comparison",
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}, Overview: coverageOverview,
				Tiers: []EcoTiers{npmTiers, pypiTiers}, Releases: &Releases{Years: releaseYears, Ecos: []EcoReleases{npmReleases, pypiReleases}}, Spend: coverageSpend(mavenSpend, npmSpend)},
		},
		{
			name: "EcosystemTab",
			req:  CoverageRequest{Eco: "npm"},
			want: func() CoverageData {
				detail := npm
				// npm's tab keeps its cohorts and its share of all spend.
				return CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Eco: "npm", Ecos: []CoverageEco{maven, detail, pypi}, Detail: &detail, Overview: coverageOverview,
					Tiers: []EcoTiers{npmTiers}, Releases: &Releases{Years: releaseYears, Ecos: []EcoReleases{npmReleases}}, Spend: coverageSpend(npmSpend)}
			}(),
		},
		{
			name: "UnknownTabFallsBack",
			req:  CoverageRequest{Eco: "cratesio"},
			want: CoverageData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecos: []CoverageEco{maven, npm, pypi}, Overview: coverageOverview,
				Tiers: []EcoTiers{npmTiers, pypiTiers}, Releases: &Releases{Years: releaseYears, Ecos: []EcoReleases{npmReleases, pypiReleases}}, Spend: coverageSpend(mavenSpend, npmSpend)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoveragePage(context.Background(), tc.req, &Deps{Analytics: coverageFixture(t)})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(&tc.want, got, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
				t.Errorf("CoveragePage diff (-want +got):\n%s", diff)
			}
			if err := CoverageTmpl.Execute(io.Discard, got); err != nil {
				t.Errorf("rendering: %v", err)
			}
		})
	}
}

func TestCoverageRendersCohorts(t *testing.T) {
	got, err := CoveragePage(context.Background(), CoverageRequest{}, &Deps{Analytics: coverageFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := CoverageTmpl.Execute(&buf, got); err != nil {
		t.Fatal(err)
	}
	// A tier past the ecosystem's size reads All N, and a weak tier is marked.
	for _, want := range []string{`>Top 100<`, `>All 300<`, `tier-row weak`, `data-chart="releases"`, `id="releases"`, `cost-llm`, `>69%<`, `10.8 core-h`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rendered coverage lacks %s", want)
		}
	}
}

func TestTierLabel(t *testing.T) {
	for _, tc := range []struct {
		tier TierBand
		want string
	}{
		{TierBand{Tier: 100, Packages: 100}, "Top 100"},
		{TierBand{Tier: 1000, Packages: 1000}, "Top 1k"},
		{TierBand{Tier: 10000, Packages: 5000}, "All 5k"},
		{TierBand{Tier: 10000, Packages: 3973}, "All 4k"},
		{TierBand{Tier: 10000, Packages: 1250}, "All 1.3k"},
		{TierBand{Tier: 1000, Packages: 300}, "All 300"},
	} {
		if got := tc.tier.Label(); got != tc.want {
			t.Errorf("Label() of %d of top %d = %q, want %q", tc.tier.Packages, tc.tier.Tier, got, tc.want)
		}
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
