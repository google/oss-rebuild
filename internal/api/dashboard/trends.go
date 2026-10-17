// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// trendWeeks bounds the weekly history shown on an ecosystem tab.
const trendWeeks = 12

var _ api.HandlerFn[CoverageRequest, CoverageData, *Deps] = CoveragePage

type CoverageRequest struct {
	Eco string // ecosystem tab; empty compares every ecosystem
}

func (CoverageRequest) Validate() error { return nil }

// CoverageWeek is one week of an ecosystem's funnel, cumulative over the
// packages attempted by the end of that week.
type CoverageWeek struct {
	Week        string
	Attempted   int64
	Reproduced  int64 // attested at their latest attempted version
	Behind      int64 // attested an earlier version but not the latest
	SharePct    int64 // Reproduced over Attempted
	WeightedPct int64 // the same, weighting each package by its score
	HasWeighted bool
	NewlyBroken int64
}

// StageDone counts the campaigns one stage attested.
type StageDone struct {
	Stage   scheduler.Stage
	Done    int64
	LLMDone int64 // agent wins at iteration 2 or later
}

// InferenceDone is the part of Done won at the agent's iteration 1.
func (d StageDone) InferenceDone() int64 { return d.Done - d.LLMDone }

// SplitsWins reports whether the stage distinguishes inference-rescue from
// LLM-diagnose wins. Only the agent stage runs both.
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
	Weeks       []CoverageWeek
}

// Campaigns counts the ecosystem's campaigns in every state.
func (e CoverageEco) Campaigns() int64 { return e.Queued + e.InFlight + e.NeedsTriage + e.Done }

type CoverageData struct {
	Loaded   bool
	AsOf     string
	Eco      string        // selected tab, empty is the comparison view
	Ecos     []CoverageEco // one per ecosystem, in name order
	Detail   *CoverageEco  // the selected ecosystem with its weekly trend
	Overview *Overview
	Tiers    []EcoTiers // popularity tiers, by ecosystem
	Releases *Releases  // release cohorts, nil if none enqueued
	Spend    *Spend     // resource mix, nil without spend
}

func coverageWeekFromStmt(stmt *sqlite3.Stmt) CoverageWeek {
	w := CoverageWeek{
		Week:        stmt.ColumnText(0),
		Attempted:   stmt.ColumnInt64(1),
		Reproduced:  stmt.ColumnInt64(2),
		Behind:      stmt.ColumnInt64(3),
		SharePct:    int64(100 * stmt.ColumnFloat(4)),
		NewlyBroken: stmt.ColumnInt64(6),
	}
	if weighted := stmt.ColumnFloat(5); weighted > 0 {
		w.WeightedPct = int64(100 * weighted)
		w.HasWeighted = true
	}
	return w
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
	byEco := map[string]*CoverageEco{}
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
	doneByStage := map[string]map[scheduler.Stage]int64{}
	llmDoneByStage := map[string]map[scheduler.Stage]int64{}
	// agent_win_kind splits a stage's done count into inference-rescue and
	// llm-diagnose. session_id is unique, so the LEFT JOIN keeps count(*).
	err = forEachRow(deps.Analytics, `
		SELECT c.ecosystem, c.state, c.stage, count(*),
			sum(CASE WHEN s.agent_win_kind = 'llm-diagnose' THEN 1 ELSE 0 END)
		FROM campaigns c
		LEFT JOIN agent_sessions s ON s.session_id = c.last_session
		GROUP BY 1, 2, 3`,
		func(stmt *sqlite3.Stmt) error {
			e := eco(stmt.ColumnText(0))
			e.HasQueue = true
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
				if doneByStage[e.Ecosystem] == nil {
					doneByStage[e.Ecosystem] = map[scheduler.Stage]int64{}
					llmDoneByStage[e.Ecosystem] = map[scheduler.Stage]int64{}
				}
				doneByStage[e.Ecosystem][st] += n
				llmDoneByStage[e.Ecosystem][st] += stmt.ColumnInt64(4)
			}
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying campaigns")
	}
	for name, e := range byEco {
		if !e.HasQueue {
			continue
		}
		for _, s := range ladder() {
			e.DoneByStage = append(e.DoneByStage, StageDone{Stage: s, Done: doneByStage[name][s], LLMDone: llmDoneByStage[name][s]})
		}
	}
	// An unknown tab (stale link) falls back to the comparison view. The
	// membership check also makes the interpolation below safe.
	if e := byEco[req.Eco]; e != nil {
		data.Eco = req.Eco
		data.Detail = e
		err = forEachRow(deps.Analytics, `
			SELECT week, packages_attempted, packages_current, packages_stale,
				coverage, coalesce(weighted_coverage, 0), newly_broken
			FROM coverage_weekly WHERE ecosystem = '`+req.Eco+`'
			ORDER BY week DESC LIMIT `+strconv.Itoa(trendWeeks),
			func(stmt *sqlite3.Stmt) error {
				e.Weeks = append(e.Weeks, coverageWeekFromStmt(stmt))
				return nil
			})
		if err != nil {
			return nil, errors.Wrap(err, "querying coverage")
		}
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
