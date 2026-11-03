// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"math"
	"strconv"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// TierBand counts an ecosystem's top Tier tracked packages by score in each
// coverage state. A tier larger than the tracked set holds all Packages.
type TierBand struct {
	Ecosystem     string
	Tier          int64
	Packages      int64
	Reproduced    int64
	NotReproduced int64
	Retrying      int64
	InFlight      int64
	Queued        int64
}

// Pct is Reproduced over the attempted packages in the tier, in whole percent.
func (t TierBand) Pct() int64 {
	if a := t.Reproduced + t.NotReproduced + t.Retrying; a > 0 {
		return 100 * t.Reproduced / a
	}
	return 0
}

// Label names the tier by its rank cutoff, or by its size when it holds the
// whole ecosystem.
func (t TierBand) Label() string {
	if t.Packages < t.Tier {
		return "All " + thousands(t.Packages)
	}
	return "Top " + thousands(t.Tier)
}

// thousands renders n past 1,000 in thousands with a k suffix, to one
// decimal: 5000 as 5k, 3973 as 4k, 1250 as 1.3k.
func thousands(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	return strconv.FormatFloat(math.Round(float64(n)/100)/10, 'f', -1, 64) + "k"
}

// Title names the tier for its bar's readout.
func (t TierBand) Title() string { return t.Ecosystem + ", " + strings.ToLower(t.Label()) }

// EcoTiers is an ecosystem's popularity tiers, smallest first.
type EcoTiers struct {
	Ecosystem string
	Tiers     []TierBand
}

func loadTiers(a Analytics) ([]EcoTiers, error) {
	var out []EcoTiers
	err := forEachRow(a, `
		SELECT ecosystem, tier, packages, reproduced, not_reproduced, retrying, in_flight, queued
		FROM popularity_tiers ORDER BY 1, 2`,
		func(stmt *sqlite3.Stmt) error {
			t := TierBand{
				Ecosystem:     stmt.ColumnText(0),
				Tier:          stmt.ColumnInt64(1),
				Packages:      stmt.ColumnInt64(2),
				Reproduced:    stmt.ColumnInt64(3),
				NotReproduced: stmt.ColumnInt64(4),
				Retrying:      stmt.ColumnInt64(5),
				InFlight:      stmt.ColumnInt64(6),
				Queued:        stmt.ColumnInt64(7),
			}
			if len(out) == 0 || out[len(out)-1].Ecosystem != t.Ecosystem {
				out = append(out, EcoTiers{Ecosystem: t.Ecosystem})
			}
			out[len(out)-1].Tiers = append(out[len(out)-1].Tiers, t)
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying popularity tiers")
	}
	return out, nil
}

// releaseFloor is the first release year charted on its own. Earlier
// releases are few and fold into its column.
const releaseFloor = 2014

// Releases backs the release cohort chart: every ecosystem's versions by
// release year, on shared year columns.
type Releases struct {
	Years []string // column labels, oldest first
	Ecos  []EcoReleases
}

// EcoReleases is one ecosystem's chart row. Counts holds one [reproduced, not
// reproduced, retrying, in flight, queued] per Years column.
type EcoReleases struct {
	Ecosystem string
	Counts    [][5]int64
}

func loadReleases(a Analytics) (*Releases, error) {
	type cell struct {
		eco  string
		year int
		c    [5]int64
	}
	var cells []cell
	last := releaseFloor
	err := forEachRow(a, `
		SELECT ecosystem, release_year, reproduced, not_reproduced, retrying, in_flight, queued
		FROM release_cohorts ORDER BY 1, 2`,
		func(stmt *sqlite3.Stmt) error {
			y, err := strconv.Atoi(stmt.ColumnText(1))
			if err != nil {
				return errors.Wrapf(err, "parsing release year %q", stmt.ColumnText(1))
			}
			c := cell{eco: stmt.ColumnText(0), year: max(y, releaseFloor)}
			for i := range c.c {
				c.c[i] = stmt.ColumnInt64(2 + i)
			}
			last = max(last, c.year)
			cells = append(cells, c)
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying release cohorts")
	}
	if len(cells) == 0 {
		return nil, nil
	}
	r := &Releases{}
	for y := releaseFloor; y <= last; y++ {
		label := strconv.Itoa(y)
		if y == releaseFloor {
			label = "≤" + label
		}
		r.Years = append(r.Years, label)
	}
	for _, c := range cells {
		if len(r.Ecos) == 0 || r.Ecos[len(r.Ecos)-1].Ecosystem != c.eco {
			r.Ecos = append(r.Ecos, EcoReleases{Ecosystem: c.eco, Counts: make([][5]int64, len(r.Years))})
		}
		row := &r.Ecos[len(r.Ecos)-1].Counts[c.year-releaseFloor]
		for i := range row {
			row[i] += c.c[i]
		}
	}
	return r, nil
}

// spendDays is the resource mix window, ending on the snapshot's latest day.
const spendDays = 7

// Core-hours convert each resource to n2 vCPU-hours at list price: private
// pool build-seconds, 4 vCPU guests, and inference request-seconds billed as a
// whole 4 vCPU, 16 GiB Cloud Run instance (an upper bound under concurrency).
// Prices are us-central1 list:
//   - n2 vCPUs: https://cloud.google.com/compute/vm-instance-pricing
//   - Build minutes: https://cloud.google.com/build/pricing
//   - Cloud Run instances: https://cloud.google.com/run/pricing
//   - Gemini tokens: https://cloud.google.com/vertex-ai/generative-ai/pricing
const (
	coreHourUSD           = 0.194236 / 4 // n2-standard-4 per vCPU
	builderCoreHPerSecond = 0.012 / 60 / coreHourUSD
	inferCoreHPerSecond   = (4*0.000018 + 16*0.000002) / coreHourUSD
	guestCoreHPerSecond   = 4.0 / 3600
	inputCoreHPerToken    = 0.75e-6 / coreHourUSD
	cachedCoreHPerToken   = 0.075e-6 / coreHourUSD
	outputCoreHPerToken   = 3.75e-6 / coreHourUSD
)

// EcoSpend is what an ecosystem spent over the spend window in core-hours,
// and the versions it reproduced for the first time.
type EcoSpend struct {
	Ecosystem   string
	Builder     float64
	Inference   float64
	Guests      float64
	LLM         float64
	NewVersions int64
	Share       int64 // percent of all core-hours
}

func (s EcoSpend) Total() float64 { return s.Builder + s.Inference + s.Guests + s.LLM }

func (s EcoSpend) PerVersion() string {
	if s.NewVersions == 0 {
		return ""
	}
	return strconv.FormatFloat(s.Total()/float64(s.NewVersions), 'f', 1, 64)
}

// Spend is the resource mix over the spend window.
type Spend struct {
	From, To string // days, inclusive
	Ecos     []EcoSpend
}

func loadSpend(a Analytics) (*Spend, error) {
	s := &Spend{}
	err := forEachRow(a, `
		WITH bounds AS (SELECT max(day) AS last FROM ecosystem_daily WHERE day != '')
		SELECT ecosystem, date(last, '-`+strconv.Itoa(spendDays-1)+` days'), last,
			sum(builder_seconds), sum(inference_seconds), sum(scratch_vm_seconds),
			sum(input_tokens), sum(cached_input_tokens), sum(output_tokens), sum(first_version_successes)
		FROM ecosystem_daily, bounds
		WHERE day != '' AND day >= date(last, '-`+strconv.Itoa(spendDays-1)+` days')
		GROUP BY 1 ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			s.From, s.To = stmt.ColumnText(1), stmt.ColumnText(2)
			input, cached := stmt.ColumnFloat(6), stmt.ColumnFloat(7)
			s.Ecos = append(s.Ecos, EcoSpend{
				Ecosystem:   stmt.ColumnText(0),
				Builder:     stmt.ColumnFloat(3) * builderCoreHPerSecond,
				Inference:   stmt.ColumnFloat(4) * inferCoreHPerSecond,
				Guests:      stmt.ColumnFloat(5) * guestCoreHPerSecond,
				LLM:         (input-cached)*inputCoreHPerToken + cached*cachedCoreHPerToken + stmt.ColumnFloat(8)*outputCoreHPerToken,
				NewVersions: stmt.ColumnInt64(9),
			})
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying spend")
	}
	if len(s.Ecos) == 0 {
		return nil, nil
	}
	var all float64
	for _, e := range s.Ecos {
		all += e.Total()
	}
	for i := range s.Ecos {
		if all > 0 {
			s.Ecos[i].Share = int64(100*s.Ecos[i].Total()/all + 0.5)
		}
	}
	return s, nil
}

func onlyEco[T any](xs []T, eco string, name func(T) string) []T {
	if eco == "" {
		return xs
	}
	var out []T
	for _, x := range xs {
		if name(x) == eco {
			out = append(out, x)
		}
	}
	return out
}
