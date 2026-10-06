// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// EcoBand is one ecosystem's card in a page's overview band.
type EcoBand struct {
	Ecosystem  string
	InScope    int64
	Started    int64
	Reproduced int64      // attested at latest attempted version
	EverBuilt  int64      // any version attested
	Growth     [][2]int64 // [unix hour, packages ever attested]
}

// Pct is Reproduced over InScope, in whole percent.
func (e EcoBand) Pct() int64 {
	if e.InScope == 0 {
		return 0
	}
	return 100 * e.Reproduced / e.InScope
}

// Pending counts the started packages not reproduced.
func (e EcoBand) Pending() int64 { return max(0, e.Started-e.Reproduced) }

// Unstarted counts the in-scope packages not started.
func (e EcoBand) Unstarted() int64 { return max(0, e.InScope-e.Started) }

// DayStat is one day of completed builds and their cost.
type DayStat struct {
	Day          string
	Attempts     int64
	Succeeded    int64
	Failed       int64
	Tokens       int64
	BuilderHours float64
	VMHours      float64
}

// Activity is a run of days for the daily charts, which draw the days
// without builds between Start and End as empty.
type Activity struct {
	Start, End int64 // unix seconds
	Days       []DayStat
}

// loadActivity sums ecosystem_daily per day over the rows matching where,
// oldest first. Start is the first such day and End the snapshot's freshness.
func loadActivity(a Analytics, where string) (Activity, error) {
	act := Activity{End: a.Freshness().Unix()}
	err := forEachRow(a, `
		SELECT day, sum(attempts), sum(successes), sum(tokens), sum(builder_seconds), sum(scratch_vm_seconds)
		FROM ecosystem_daily WHERE day != '' AND `+where+` GROUP BY 1 ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			d := DayStat{
				Day:          stmt.ColumnText(0),
				Attempts:     stmt.ColumnInt64(1),
				Succeeded:    stmt.ColumnInt64(2),
				Tokens:       stmt.ColumnInt64(3),
				BuilderHours: stmt.ColumnFloat(4) / 3600,
				VMHours:      stmt.ColumnFloat(5) / 3600,
			}
			d.Failed = d.Attempts - d.Succeeded
			act.Days = append(act.Days, d)
			return nil
		})
	if err != nil {
		return act, errors.Wrap(err, "querying daily activity")
	}
	if len(act.Days) > 0 {
		t, err := time.Parse(time.DateOnly, act.Days[0].Day)
		if err != nil {
			return act, errors.Wrap(err, "parsing day")
		}
		act.Start = t.Unix()
	}
	return act, nil
}

// Overview backs the home and coverage pages' header band.
type Overview struct {
	AsOf       string
	InScope    int64
	Started    int64
	Reproduced int64
	Ecos       []EcoBand
	Activity   Activity // every day of the build history
}

// Total folds the ecosystems into one band.
func (o Overview) Total() EcoBand {
	return EcoBand{Ecosystem: "All ecosystems", InScope: o.InScope, Started: o.Started, Reproduced: o.Reproduced}
}

// Only selects one ecosystem's card, or every card for an empty eco.
func (o Overview) Only(eco string) []EcoBand {
	if eco == "" {
		return o.Ecos
	}
	for _, e := range o.Ecos {
		if e.Ecosystem == eco {
			return []EcoBand{e}
		}
	}
	return nil
}

// loadOverview returns nil when no ecosystem has published signals, since
// there is no scope to measure.
func loadOverview(a Analytics) (*Overview, error) {
	o := &Overview{AsOf: asOf(a)}
	index := map[string]int{}
	err := forEachRow(a, `
		SELECT c.ecosystem, c.universe_packages, c.tracked_packages, c.current_packages, coalesce(p.built, 0)
		FROM signal_coverage c
		LEFT JOIN (SELECT ecosystem, sum(ever_built) AS built FROM package_stats GROUP BY 1) p ON p.ecosystem = c.ecosystem
		ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			e := EcoBand{
				Ecosystem:  stmt.ColumnText(0),
				InScope:    stmt.ColumnInt64(1),
				Started:    stmt.ColumnInt64(2),
				Reproduced: stmt.ColumnInt64(3),
				EverBuilt:  stmt.ColumnInt64(4),
			}
			index[e.Ecosystem] = len(o.Ecos)
			o.Ecos = append(o.Ecos, e)
			o.InScope += e.InScope
			o.Started += e.Started
			o.Reproduced += e.Reproduced
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying standing")
	}
	if len(o.Ecos) == 0 {
		return nil, nil
	}
	// Hour buckets keep the series small.
	err = forEachRow(a, `
		WITH firsts AS (
			SELECT ecosystem, min(first_succeeded_at) AS t FROM version_stats
			WHERE first_succeeded_at IS NOT NULL GROUP BY ecosystem, package)
		SELECT ecosystem, CAST(strftime('%s', substr(t, 1, 13) || ':00:00') AS INTEGER), count(*)
		FROM firsts GROUP BY 1, 2 ORDER BY 1, 2`,
		func(stmt *sqlite3.Stmt) error {
			i, ok := index[stmt.ColumnText(0)]
			if !ok {
				return nil
			}
			e := &o.Ecos[i]
			n := stmt.ColumnInt64(2)
			if len(e.Growth) > 0 {
				n += e.Growth[len(e.Growth)-1][1]
			}
			e.Growth = append(e.Growth, [2]int64{stmt.ColumnInt64(1), n})
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying first reproductions")
	}
	o.Activity, err = loadActivity(a, "1")
	if err != nil {
		return nil, err
	}
	return o, nil
}
