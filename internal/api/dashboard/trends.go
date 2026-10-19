// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"maps"
	"slices"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[CoverageRequest, CoverageData, *Deps] = CoveragePage

type CoverageRequest struct {
	Eco string // ecosystem tab; empty compares every ecosystem
}

func (CoverageRequest) Validate() error { return nil }

// StageDone counts the campaigns one stage attested.
type StageDone struct {
	Stage   scheduler.Stage
	Done    int64
	LLMDone int64 // the part of Done attested with a strategy the agent proposed or handed off
}

// InferenceDone counts the rest of Done, attested with a strategy from
// inference or a sibling version's seed.
func (d StageDone) InferenceDone() int64 { return d.Done - d.LLMDone }

// SplitsWins reports whether the stage splits Done between inference and the
// agent. Only the agent stage runs both.
func (d StageDone) SplitsWins() bool { return d.Stage == scheduler.StageAgent }

// CoverageEco is one ecosystem's standing, counted as EcoBand counts it, and
// the state of its onboarding queue.
type CoverageEco struct {
	Ecosystem   string
	HasScope    bool // false until the ecosystem's signals are published
	InScope     int64
	Attempted   int64
	Reproduced  int64
	SuccessPct  int64 // Reproduced over Attempted
	WeightedPct int64 // the same, weighting each package by its score
	HasQueue    bool  // false until the ecosystem has campaigns
	Queued      int64 // campaigns, which count package versions
	InFlight    int64
	NeedsTriage int64
	Done        int64
	DoneByStage []StageDone // ladder order, every stage present
}

// Campaigns counts the ecosystem's campaigns in every state.
func (e CoverageEco) Campaigns() int64 { return e.Queued + e.InFlight + e.NeedsTriage + e.Done }

// Diagnosed counts the done campaigns an agent's own build reproduced.
func (e CoverageEco) Diagnosed() int64 {
	var n int64
	for _, d := range e.DoneByStage {
		n += d.LLMDone
	}
	return n
}

// Inferred counts the done campaigns no LLM build reproduced: replays,
// inference, and agent sessions won by inference or a sibling's seed.
func (e CoverageEco) Inferred() int64 { return e.Done - e.Diagnosed() }

type CoverageData struct {
	Frame
	Loaded   bool
	AsOf     string
	Eco      string        // selected tab, empty is the comparison view
	Ecos     []CoverageEco // one per ecosystem, in name order
	Detail   *CoverageEco  // the selected ecosystem
	Overview *Overview
	Tiers    []EcoTiers // popularity tiers, by ecosystem
	Releases *Releases  // release cohorts, nil if none enqueued
	Spend    *Spend     // resource mix, nil without spend
}

// ladder lists the stages cheapest first, as Stage.Next walks them.
func ladder() []scheduler.Stage {
	out := []scheduler.Stage{scheduler.StageReplay}
	for s, ok := scheduler.StageReplay.Next(); ok; s, ok = s.Next() {
		out = append(out, s)
	}
	return out
}

func CoveragePage(ctx context.Context, req CoverageRequest, deps *Deps) (*CoverageData, error) {
	data := &CoverageData{}
	if deps.Analytics == nil {
		return data, nil
	}
	data.Loaded = true
	data.AsOf = asOf(deps.Analytics)
	overview, err := loadOverview(deps.Analytics)
	if err != nil {
		return nil, errors.Wrap(err, "loading overview")
	}
	data.Overview = overview
	if data.Tiers, err = loadTiers(deps.Analytics); err != nil {
		return nil, err
	}
	if data.Releases, err = loadReleases(deps.Analytics); err != nil {
		return nil, err
	}
	if data.Spend, err = loadSpend(deps.Analytics); err != nil {
		return nil, err
	}
	byEco, err := loadCampaigns(deps.Analytics)
	if err != nil {
		return nil, err
	}
	eco := func(name string) *CoverageEco {
		if byEco[name] == nil {
			byEco[name] = &CoverageEco{Ecosystem: name}
		}
		return byEco[name]
	}
	// Ecosystems with attempts but no signals or campaigns still get a tab
	// for their weekly funnel.
	err = forEachRow(deps.Analytics, `SELECT DISTINCT ecosystem FROM coverage_weekly`,
		func(stmt *sqlite3.Stmt) error {
			eco(stmt.ColumnText(0))
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying ecosystems")
	}
	err = forEachRow(deps.Analytics, `
		SELECT ecosystem, tracked_packages, attempted_packages, current_packages, attempted_mass, current_mass
		FROM signal_coverage`,
		func(stmt *sqlite3.Stmt) error {
			e := eco(stmt.ColumnText(0))
			e.HasScope = true
			e.InScope, e.Attempted, e.Reproduced = stmt.ColumnInt64(1), stmt.ColumnInt64(2), stmt.ColumnInt64(3)
			if e.Attempted > 0 {
				e.SuccessPct = 100 * e.Reproduced / e.Attempted
			}
			if mass := stmt.ColumnFloat(4); mass > 0 {
				e.WeightedPct = int64(100 * stmt.ColumnFloat(5) / mass)
			}
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying signal coverage")
	}
	// An unknown tab (stale link) falls back to the comparison view. The
	// membership check also makes the interpolation below safe.
	if e := byEco[req.Eco]; e != nil {
		data.Eco = req.Eco
		data.Detail = e
		// An ecosystem tab shows only its own cohorts. Spend shares stay
		// shares of every ecosystem's spend.
		data.Tiers = onlyEco(data.Tiers, req.Eco, func(t EcoTiers) string { return t.Ecosystem })
		if data.Releases != nil {
			data.Releases.Ecos = onlyEco(data.Releases.Ecos, req.Eco, func(r EcoReleases) string { return r.Ecosystem })
		}
		if data.Spend != nil {
			data.Spend.Ecos = onlyEco(data.Spend.Ecos, req.Eco, func(s EcoSpend) string { return s.Ecosystem })
		}
	}
	for _, name := range slices.Sorted(maps.Keys(byEco)) {
		data.Ecos = append(data.Ecos, *byEco[name])
	}
	return data, nil
}

// loadCampaigns counts each ecosystem's campaigns by state, and the done ones
// by the stage that attested them, keyed by ecosystem.
func loadCampaigns(a Analytics) (map[string]*CoverageEco, error) {
	byEco := map[string]*CoverageEco{}
	// The last column counts the campaigns the agent stage attested with a
	// strategy the agent proposed or handed off, as the session's WonBy
	// records. The CASE reads a session only for a campaign the agent stage
	// attested, since a session's record is large.
	// NOTE: A session from before WonBy counts when it reproduced after its
	// first iteration or with a flow or manual strategy, which inference
	// never writes.
	err := forEachRow(a, `
		SELECT c.ecosystem, c.state, c.stage, count(*),
			sum(CASE WHEN c.state = 'DONE' AND c.stage = 'agent' AND coalesce(
				(SELECT nullif(s.won_by, '') FROM agent_sessions s WHERE s.session_id = c.last_session),
				CASE WHEN EXISTS (
					SELECT 1 FROM agent_iterations i
					WHERE i.session_id = c.last_session AND i.status = 'SUCCESS' AND (i.number >= 2
						OR json_extract(i.raw, '$.Strategy.flow') IS NOT NULL
						OR json_extract(i.raw, '$.Strategy.manual') IS NOT NULL)) THEN 'agent' END) = 'agent' THEN 1 ELSE 0 END)
		FROM campaigns c
		GROUP BY 1, 2, 3`,
		func(stmt *sqlite3.Stmt) error {
			name := stmt.ColumnText(0)
			e := byEco[name]
			if e == nil {
				e = &CoverageEco{Ecosystem: name, HasQueue: true}
				for _, s := range ladder() {
					e.DoneByStage = append(e.DoneByStage, StageDone{Stage: s})
				}
				byEco[name] = e
			}
			n := stmt.ColumnInt64(3)
			switch scheduler.State(stmt.ColumnText(1)) {
			case scheduler.StateQueued:
				e.Queued += n
			case scheduler.StateInFlight:
				e.InFlight += n
			case scheduler.StateNeedsTriage:
				e.NeedsTriage += n
			case scheduler.StateDone:
				// A done campaign's stage is the one that attested it.
				e.Done += n
				st := scheduler.Stage(stmt.ColumnText(2))
				if i := slices.IndexFunc(e.DoneByStage, func(d StageDone) bool { return d.Stage == st }); i >= 0 {
					e.DoneByStage[i].Done += n
					e.DoneByStage[i].LLMDone += stmt.ColumnInt64(4)
				}
			}
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying campaigns")
	}
	return byEco, nil
}
