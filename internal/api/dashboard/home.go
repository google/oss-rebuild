// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// daySeconds is one day in the unix seconds the charts plot.
const daySeconds = int64(24 * time.Hour / time.Second)

// daily holds a running count over the days that added nothing, one point
// per day from start through end.
func daily(steps [][2]int64, start, end int64) [][2]float64 {
	var out [][2]float64
	var n int64
	for t := start; t <= end; t += daySeconds {
		for len(steps) > 0 && steps[0][0] <= t {
			n, steps = steps[0][1], steps[1:]
		}
		out = append(out, [2]float64{float64(t), float64(n)})
	}
	return out
}

// attentionLimit bounds the give-ups the home page lists.
const attentionLimit = 5

// loadAttention reads the give-ups awaiting triage with the highest campaign
// scores, one per package, and counts the packages that have one.
func loadAttention(a Analytics) ([]SessionView, int64, error) {
	var out []SessionView
	var packages int64
	err := forEachRow(a, `
		SELECT raw, count(*) OVER () FROM (
			SELECT s.raw, c.ecosystem, c.package, c.score,
				row_number() OVER (PARTITION BY c.ecosystem, c.package ORDER BY c.score DESC, c.version DESC) AS rn
			FROM campaigns c JOIN agent_sessions s ON s.session_id = c.last_session
			WHERE c.state = '`+string(scheduler.StateNeedsTriage)+`' AND c.outcome = '`+string(scheduler.OutcomeGaveUp)+`')
		WHERE rn = 1 ORDER BY score DESC, ecosystem, package LIMIT `+strconv.Itoa(attentionLimit),
		func(stmt *sqlite3.Stmt) error {
			var s schema.AgentSession
			if err := json.Unmarshal([]byte(stmt.ColumnText(0)), &s); err != nil {
				return errors.Wrap(err, "decoding agent session")
			}
			out = append(out, NewSessionView(s))
			packages = stmt.ColumnInt64(1)
			return nil
		})
	return out, packages, errors.Wrap(err, "querying give-ups")
}

// Home backs the home page's panels, all from the snapshot's derived tables.
type Home struct {
	Reproduced Reproduced
	Cumulative *Chart        // Reproduced's running counts by day, nil before the first reproduction
	Activity   Activity      // every day of the build history
	Campaigns  []CoverageEco // the ecosystems with campaigns, in name order
	Attention  []SessionView // the give-ups awaiting triage with the highest scores, one per package
	GaveUp     int64         // packages with a give-up awaiting triage
}

// CampaignShare is n as a percent of the most campaigns any ecosystem has,
// which sizes each ecosystem's campaign bar.
func (h Home) CampaignShare(n int64) float64 {
	peak := int64(1)
	for _, e := range h.Campaigns {
		peak = max(peak, e.Campaigns())
	}
	return 100 * float64(n) / float64(peak)
}

// loadHome reads the home page's panels. It returns nil for a snapshot
// without completed builds or signals.
func loadHome(a Analytics) (*Home, error) {
	r, err := loadReproduced(a)
	if err != nil {
		return nil, err
	}
	if len(r.Ecos) == 0 {
		return nil, nil
	}
	h := &Home{Reproduced: r}
	if len(r.Versions) > 0 {
		start := r.Versions[0][0]
		end := max(a.Freshness().UTC().Truncate(24*time.Hour).Unix(), r.Versions[len(r.Versions)-1][0])
		h.Cumulative = &Chart{Start: start, End: end, Step: daySeconds, Fmt: "num", EndLabels: true, Series: []ChartSeries{
			{Label: "versions", Class: "ok", Kind: "area", Points: daily(r.Versions, start, end)},
			{Label: "packages", Class: "ok-deep", Kind: "dash", Points: daily(r.Packages, start, end)},
		}}
	}
	if h.Activity, err = loadActivity(a, "1"); err != nil {
		return nil, err
	}
	campaigns, err := loadCampaigns(a)
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(campaigns)) {
		h.Campaigns = append(h.Campaigns, *campaigns[name])
	}
	if h.Attention, h.GaveUp, err = loadAttention(a); err != nil {
		return nil, err
	}
	return h, nil
}
