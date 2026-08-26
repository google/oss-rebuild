// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[ResourcesRequest, ResourcesData, *Deps] = ResourcesPage

// humanCount renders token counts at display precision: 19M, 1.2M, 450K.
func humanCount(n int) string {
	format := func(v float64, unit string) string {
		if v >= 10 {
			return fmt.Sprintf("%d%s", int(v+0.5), unit)
		}
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + unit
	}
	switch {
	case n >= 1_000_000_000:
		return format(float64(n)/1e9, "B")
	case n >= 1_000_000:
		return format(float64(n)/1e6, "M")
	case n >= 1_000:
		return format(float64(n)/1e3, "K")
	default:
		return fmt.Sprintf("%d", n)
	}
}

type ResourcesRequest struct {
	Eco string // ecosystem tab for the daily table; empty aggregates every ecosystem
}

func (ResourcesRequest) Validate() error { return nil }

// EcoDaily is one ecosystem-day in the trends table.
type EcoDaily struct {
	Day            string
	Attempts       int64
	Successes      int64
	SuccessPct     int64
	AttemptsBarPct int64
	BuilderMinutes float64
	Tokens         string
	VMMinutes      float64
}

// ExpensivePackage is one row of the most-expensive-packages table, costed
// by summed builder time over the window.
type ExpensivePackage struct {
	Ecosystem      string
	Package        string
	Attempts       int64
	BuilderMinutes float64
	BarPct         int64
}

type ResourcesData struct {
	Loaded     bool
	AsOf       string
	WindowDays int

	TotalAttempts     int64
	TotalSuccesses    int64
	TotalTokens       string
	TotalBuilderHours float64
	TotalVMHours      float64

	Ecosystems  []string // tab labels, from the window's data
	Eco         string   // selected tab; empty is the aggregate view
	Days        []EcoDaily
	TopPackages []ExpensivePackage
}

func ResourcesPage(ctx context.Context, req ResourcesRequest, deps *Deps) (*ResourcesData, error) {
	data := &ResourcesData{WindowDays: analyticsWindowDays}
	if deps.Analytics == nil {
		return data, nil
	}
	a := deps.Analytics
	data.Loaded = true
	data.AsOf = asOf(a)
	dayBound := fmt.Sprintf("date('now','-%d days')", analyticsWindowDays)
	err := forEachRow(a, `
		SELECT coalesce(sum(attempts),0), coalesce(sum(successes),0), coalesce(sum(tokens),0),
			coalesce(sum(builder_seconds),0), coalesce(sum(scratch_vm_seconds),0)
		FROM ecosystem_daily WHERE day >= `+dayBound,
		func(stmt *sqlite3.Stmt) error {
			data.TotalAttempts = stmt.ColumnInt64(0)
			data.TotalSuccesses = stmt.ColumnInt64(1)
			data.TotalTokens = humanCount(int(stmt.ColumnInt64(2)))
			data.TotalBuilderHours = stmt.ColumnFloat(3) / 3600
			data.TotalVMHours = stmt.ColumnFloat(4) / 3600
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying totals")
	}
	err = forEachRow(a, `SELECT DISTINCT ecosystem FROM ecosystem_daily WHERE day >= `+dayBound+` ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			data.Ecosystems = append(data.Ecosystems, stmt.ColumnText(0))
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying ecosystems")
	}
	// An unknown tab (stale link) falls back to the aggregate view. The
	// membership check also makes the interpolation below safe.
	if slices.Contains(data.Ecosystems, req.Eco) {
		data.Eco = req.Eco
	}
	daily := `
		SELECT day, sum(attempts), sum(successes), sum(builder_seconds), sum(tokens), sum(scratch_vm_seconds)
		FROM ecosystem_daily WHERE day >= ` + dayBound
	if data.Eco != "" {
		daily += ` AND ecosystem = '` + data.Eco + `'`
	}
	daily += ` GROUP BY day ORDER BY day DESC`
	var maxAttempts int64 = 1
	err = forEachRow(a, daily,
		func(stmt *sqlite3.Stmt) error {
			d := EcoDaily{
				Day:            stmt.ColumnText(0),
				Attempts:       stmt.ColumnInt64(1),
				Successes:      stmt.ColumnInt64(2),
				BuilderMinutes: stmt.ColumnFloat(3) / 60,
				Tokens:         humanCount(int(stmt.ColumnInt64(4))),
				VMMinutes:      stmt.ColumnFloat(5) / 60,
			}
			if d.Attempts > 0 {
				d.SuccessPct = 100 * d.Successes / d.Attempts
			}
			maxAttempts = max(maxAttempts, d.Attempts)
			data.Days = append(data.Days, d)
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying daily trends")
	}
	for i := range data.Days {
		data.Days[i].AttemptsBarPct = 100 * data.Days[i].Attempts / maxAttempts
	}
	var maxCost float64 = 1
	err = forEachRow(a, `
		SELECT ecosystem, package, count(*), sum(coalesce(nullif(builder_seconds,0), build_seconds))
		FROM cost_observations
		WHERE source='attempt' AND timestamp >= '`+sqlitex.TimeColumn(time.Now().AddDate(0, 0, -analyticsWindowDays))+`'
		GROUP BY ecosystem, package
		HAVING sum(coalesce(nullif(builder_seconds,0), build_seconds)) > 0
		ORDER BY 4 DESC LIMIT 10`,
		func(stmt *sqlite3.Stmt) error {
			p := ExpensivePackage{
				Ecosystem:      stmt.ColumnText(0),
				Package:        stmt.ColumnText(1),
				Attempts:       stmt.ColumnInt64(2),
				BuilderMinutes: stmt.ColumnFloat(3) / 60,
			}
			maxCost = max(maxCost, p.BuilderMinutes)
			data.TopPackages = append(data.TopPackages, p)
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying top packages")
	}
	for i := range data.TopPackages {
		data.TopPackages[i].BarPct = int64(100 * data.TopPackages[i].BuilderMinutes / maxCost)
	}
	return data, nil
}
