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
	Stage scheduler.Stage
	Done  int64
}

// CoverageEco is one ecosystem's standing against the packages in scope and
// the state of its onboarding queue. A package is in scope when the
// published signals rank it, started once a campaign or an attempt has
// touched it, and reproduced when its latest attempted version is attested.
type CoverageEco struct {
	Ecosystem   string
	HasScope    bool // false until the ecosystem's signals are published
	InScope     int64
	Started     int64
	Reproduced  int64
	CoveragePct int64 // Reproduced over InScope
	WeightedPct int64 // the same, weighting each package by its score
	HasQueue    bool  // false until the ecosystem has campaigns
	Queued      int64 // campaigns, which count package versions
	InFlight    int64
	NeedsTriage int64
	Done        int64
	DoneByStage []StageDone // ladder order, every stage present
	Weeks       []CoverageWeek
}

type CoverageData struct {
	Loaded bool
	AsOf   string
	Eco    string        // selected tab, empty is the comparison view
	Ecos   []CoverageEco // one per ecosystem, in name order
	Detail *CoverageEco  // the selected ecosystem with its weekly trend
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
	byEco := map[string]*CoverageEco{}
	eco := func(name string) *CoverageEco {
		if byEco[name] == nil {
			byEco[name] = &CoverageEco{Ecosystem: name}
		}
		return byEco[name]
	}
	// Ecosystems with attempts but no signals or campaigns still get a tab
	// for their weekly funnel.
	err := forEachRow(deps.Analytics, `SELECT DISTINCT ecosystem FROM coverage_weekly`,
		func(stmt *sqlite3.Stmt) error {
			eco(stmt.ColumnText(0))
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying ecosystems")
	}
	err = forEachRow(deps.Analytics, `
		SELECT ecosystem, universe_packages, tracked_packages, current_packages, covered_mass_share
		FROM signal_coverage`,
		func(stmt *sqlite3.Stmt) error {
			e := eco(stmt.ColumnText(0))
			e.HasScope = true
			e.InScope, e.Started, e.Reproduced = stmt.ColumnInt64(1), stmt.ColumnInt64(2), stmt.ColumnInt64(3)
			if e.InScope > 0 {
				e.CoveragePct = 100 * e.Reproduced / e.InScope
			}
			e.WeightedPct = int64(100 * stmt.ColumnFloat(4))
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying signal coverage")
	}
	doneByStage := map[string]map[scheduler.Stage]int64{}
	err = forEachRow(deps.Analytics, `SELECT ecosystem, state, stage, count(*) FROM campaigns GROUP BY 1, 2, 3`,
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
				if doneByStage[e.Ecosystem] == nil {
					doneByStage[e.Ecosystem] = map[scheduler.Stage]int64{}
				}
				doneByStage[e.Ecosystem][scheduler.Stage(stmt.ColumnText(2))] += n
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
			e.DoneByStage = append(e.DoneByStage, StageDone{Stage: s, Done: doneByStage[name][s]})
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
	}
	for _, name := range slices.Sorted(maps.Keys(byEco)) {
		data.Ecos = append(data.Ecos, *byEco[name])
	}
	return data, nil
}
