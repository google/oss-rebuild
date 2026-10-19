// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

// homeFixture adds to coverageFixture a later reproduction, and the campaign
// outcomes and session documents behind the give-ups.
func homeFixture(t *testing.T) memAnalytics {
	a := coverageFixture(t)
	for _, s := range []string{
		`INSERT INTO version_stats VALUES ('npm', 'e', '1', '2026-08-31T09:00:00.000Z')`,
		`ALTER TABLE campaigns ADD COLUMN outcome TEXT`,
		`ALTER TABLE campaigns ADD COLUMN score REAL`,
		`ALTER TABLE agent_sessions ADD COLUMN raw TEXT`,
		// d gave up at two versions, the lower scored one hidden. g failed.
		`UPDATE campaigns SET outcome = 'GAVE_UP', score = 0.5, last_session = 's-d1' WHERE package = 'd'`,
		`INSERT INTO campaigns VALUES ('npm', 'd', '2', 'd.tgz', 'NEEDS_TRIAGE', 'agent', 's-d2', 'GAVE_UP', 0.4), ('npm', 'g', '1', 'g.tgz', 'NEEDS_TRIAGE', 'agent', 's-g', 'FAILURE', 0.9)`,
		`INSERT INTO agent_sessions VALUES
			('s-d1', NULL, '{"ID":"s-d1","Target":{"Ecosystem":"npm","Package":"d","Version":"1","Artifact":"d.tgz"},"Status":"COMPLETED","StopReason":"GAVE_UP","Summary":"Gave up: The lockfile is untracked.","Created":"2026-08-31T08:00:00Z"}'),
			('s-d2', NULL, '{"ID":"s-d2"}'), ('s-g', NULL, '{"ID":"s-g"}')`,
	} {
		if err := a.db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return a
}

func TestLoadHome(t *testing.T) {
	aug30, aug31, sep1 := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	gaveUp := schema.AgentSession{
		ID: "s-d1", Target: rebuild.Target{Ecosystem: "npm", Package: "d", Version: "1", Artifact: "d.tgz"}, Status: "COMPLETED",
		StopReason: schema.AgentCompleteReasonGaveUp, Summary: "Gave up: The lockfile is untracked.", Created: time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC),
	}
	want := &Home{
		Reproduced: Reproduced{
			// pypi has signals and no completed build.
			Ecos: []EcoReproduced{
				{Ecosystem: "npm", Versions: 4, VersionsAttempted: 5, Packages: 3, PackagesAttempted: 4, Tiers: npmTiers.Tiers},
				{Ecosystem: "pypi", Tiers: pypiTiers.Tiers},
			},
			Versions: [][2]int64{{aug30, 3}, {aug31, 4}},
			Packages: [][2]int64{{aug30, 2}, {aug31, 3}},
		},
		// The counts hold through the snapshot's day.
		Cumulative: &Chart{Start: aug30, End: sep1, Step: daySeconds, Fmt: "num", EndLabels: true, Series: []ChartSeries{
			{Label: "versions", Class: "ok", Kind: "area", Points: [][2]float64{{float64(aug30), 3}, {float64(aug31), 4}, {float64(sep1), 4}}},
			{Label: "packages", Class: "ok-deep", Kind: "dash", Points: [][2]float64{{float64(aug30), 2}, {float64(aug31), 3}, {float64(sep1), 3}}},
		}},
		Activity: coverageOverview.Activity,
		Campaigns: []CoverageEco{{
			Ecosystem: "npm", HasQueue: true, Queued: 1, InFlight: 1, NeedsTriage: 3, Done: 4,
			DoneByStage: []StageDone{{Stage: scheduler.StageReplay, Done: 1}, {Stage: scheduler.StageInfer}, {Stage: scheduler.StageAgent, Done: 3, LLMDone: 1}},
		}},
		Attention: []SessionView{NewSessionView(gaveUp)},
		GaveUp:    1,
	}
	got, err := loadHome(homeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(SessionView{}, "Encoded")); diff != "" {
		t.Errorf("loadHome diff (-want +got):\n%s", diff)
	}
	if top := got.Reproduced.Top(); top.Reproduced != 250 || top.Packages != 300 {
		t.Errorf("Top() = %+v, want 250 of 300", top)
	}
}

func TestLoadHomeEmpty(t *testing.T) {
	a := newMemAnalytics(t,
		`CREATE TABLE version_stats (ecosystem TEXT, package TEXT, version TEXT, first_succeeded_at TEXT)`,
		`CREATE TABLE popularity_tiers (ecosystem TEXT, tier INT, packages INT, reproduced INT, not_reproduced INT, retrying INT, in_flight INT, queued INT)`,
	)
	got, err := loadHome(a)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("loadHome() = %+v, want nil", got)
	}
}

func TestIndexRendersHome(t *testing.T) {
	home, err := loadHome(homeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := IndexTmpl.Execute(&buf, &IndexData{Home: home}); err != nil {
		t.Fatal(err)
	}
	// npm's top 1,000 holds its 300 tracked packages. pypi's lone tier is weak
	// and it has no top 1,000.
	for _, want := range []string{
		`>83.3%<`, `>80%</b> of 5 versions attempted`, `id="home-cumulative"`, `"EndLabels":true`,
		`href="/coverage?eco=npm"`, `<span>All 300</span>`, `class="warn" style="width: 50.0%"`, `&ndash;`,
		`data-chart="columns"`, `data-k="needs triage"`, `style="width: 100.0%"`,
		`href="/session/s-d1"`, `The lockfile is untracked.`, `<b>1</b> packages with a give-up`,
		// The replay, the inference-won, and the seed-won agent sessions are
		// inference's, the flow strategy the agent's.
		`data-v="3" data-k="attested by inference"`, `data-v="1" data-k="attested by the agent"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rendered index lacks %s", want)
		}
	}
	if strings.Contains(buf.String(), "attested at replay") {
		t.Error("rendered index splits out replays")
	}
}

func TestDaily(t *testing.T) {
	const day = float64(daySeconds)
	for _, tc := range []struct {
		name  string
		steps [][2]int64
		want  [][2]float64
	}{
		{"FillsForward", [][2]int64{{0, 2}, {2 * daySeconds, 5}}, [][2]float64{{0, 2}, {day, 2}, {2 * day, 5}, {3 * day, 5}}},
		{"NoSteps", nil, [][2]float64{{0, 0}, {day, 0}, {2 * day, 0}, {3 * day, 0}}},
		{"FirstStepAfterStart", [][2]int64{{2 * daySeconds, 5}}, [][2]float64{{0, 0}, {day, 0}, {2 * day, 5}, {3 * day, 5}}},
		{"LaterStepInADayWins", [][2]int64{{daySeconds + 60, 3}, {daySeconds + 120, 4}}, [][2]float64{{0, 0}, {day, 0}, {2 * day, 4}, {3 * day, 4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, daily(tc.steps, 0, 3*daySeconds)); diff != "" {
				t.Errorf("daily diff (-want +got):\n%s", diff)
			}
		})
	}
}
