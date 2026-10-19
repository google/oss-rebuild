// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"slices"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// topTier is the popularity tier the coverage figures quote.
const topTier = 1000

// EcoReproduced is what one ecosystem has reproduced at any version, and its
// popularity tiers.
type EcoReproduced struct {
	Ecosystem         string
	Versions          int64      // package versions with a reproduction
	VersionsAttempted int64      // package versions with a completed build
	Packages          int64      // packages with a version reproduced
	PackagesAttempted int64      // packages with a completed build
	Tiers             []TierBand // smallest first, nil without published signals
}

// VersionsPct is Versions over VersionsAttempted, in whole percent.
func (e EcoReproduced) VersionsPct() int64 { return wholePct(e.Versions, e.VersionsAttempted) }

// PackagesPct is Packages over PackagesAttempted, in whole percent.
func (e EcoReproduced) PackagesPct() int64 { return wholePct(e.Packages, e.PackagesAttempted) }

// Top is the ecosystem's top 1,000 tier, zero without published signals.
func (e EcoReproduced) Top() TierBand {
	for _, t := range e.Tiers {
		if t.Tier == topTier {
			return t
		}
	}
	return TierBand{}
}

func wholePct(n, of int64) int64 {
	if of == 0 {
		return 0
	}
	return 100 * n / of
}

// Reproduced backs the home page's headline and the sidebar's figures.
type Reproduced struct {
	Ecos     []EcoReproduced // name order
	Versions [][2]int64      // the running count of versions reproduced, as [unix day, count] for each day that added one
	Packages [][2]int64      // the same for packages with a version reproduced
}

// Total folds the ecosystems into one, without tiers.
func (r Reproduced) Total() EcoReproduced {
	t := EcoReproduced{Ecosystem: "All ecosystems"}
	for _, e := range r.Ecos {
		t.Versions += e.Versions
		t.VersionsAttempted += e.VersionsAttempted
		t.Packages += e.Packages
		t.PackagesAttempted += e.PackagesAttempted
	}
	return t
}

// Top folds the ecosystems' top 1,000 tiers into one.
func (r Reproduced) Top() TierBand {
	t := TierBand{Ecosystem: "All ecosystems", Tier: topTier}
	for _, e := range r.Ecos {
		b := e.Top()
		t.Packages += b.Packages
		t.Reproduced += b.Reproduced
		t.NotReproduced += b.NotReproduced
		t.Retrying += b.Retrying
		t.InFlight += b.InFlight
		t.Queued += b.Queued
	}
	return t
}

// loadReproduced counts the reproduced versions and packages in
// version_stats. It folds the same completed builds as package_stats, so its
// packages are package_stats' rows.
func loadReproduced(a Analytics) (Reproduced, error) {
	var r Reproduced
	err := forEachRow(a, `
		SELECT ecosystem, count(*), count(first_succeeded_at), count(DISTINCT package),
			count(DISTINCT CASE WHEN first_succeeded_at IS NOT NULL THEN package END)
		FROM version_stats GROUP BY 1 ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			r.Ecos = append(r.Ecos, EcoReproduced{
				Ecosystem:         stmt.ColumnText(0),
				VersionsAttempted: stmt.ColumnInt64(1),
				Versions:          stmt.ColumnInt64(2),
				PackagesAttempted: stmt.ColumnInt64(3),
				Packages:          stmt.ColumnInt64(4),
			})
			return nil
		})
	if err != nil {
		return r, errors.Wrap(err, "querying reproduced counts")
	}
	tiers, err := loadTiers(a)
	if err != nil {
		return r, err
	}
	// An ecosystem with signals and no completed build still has tiers.
	for _, t := range tiers {
		i := slices.IndexFunc(r.Ecos, func(e EcoReproduced) bool { return e.Ecosystem == t.Ecosystem })
		if i < 0 {
			i = len(r.Ecos)
			r.Ecos = append(r.Ecos, EcoReproduced{Ecosystem: t.Ecosystem})
		}
		r.Ecos[i].Tiers = t.Tiers
	}
	slices.SortFunc(r.Ecos, func(x, y EcoReproduced) int { return strings.Compare(x.Ecosystem, y.Ecosystem) })
	running := func(sql string) ([][2]int64, error) {
		var out [][2]int64
		var n int64
		err := forEachRow(a, sql, func(stmt *sqlite3.Stmt) error {
			n += stmt.ColumnInt64(1)
			out = append(out, [2]int64{stmt.ColumnInt64(0), n})
			return nil
		})
		return out, err
	}
	if r.Versions, err = running(`
		SELECT CAST(strftime('%s', substr(first_succeeded_at, 1, 10)) AS INTEGER), count(*) FROM version_stats
		WHERE first_succeeded_at IS NOT NULL GROUP BY 1 ORDER BY 1`); err != nil {
		return r, errors.Wrap(err, "querying reproduced versions")
	}
	if r.Packages, err = running(`
		WITH firsts AS (
			SELECT min(first_succeeded_at) AS t FROM version_stats
			WHERE first_succeeded_at IS NOT NULL GROUP BY ecosystem, package)
		SELECT CAST(strftime('%s', substr(t, 1, 10)) AS INTEGER), count(*) FROM firsts GROUP BY 1 ORDER BY 1`); err != nil {
		return r, errors.Wrap(err, "querying reproduced packages")
	}
	return r, nil
}
