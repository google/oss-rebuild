// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"slices"
	"strconv"

	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// packagesLimit bounds the /packages listing so the page stays readable.
// The count cards still cover every package in the selected scope.
const packagesLimit = 500

var _ api.HandlerFn[PackagesRequest, PackagesData, *Deps] = PackagesPage

type PackagesRequest struct {
	Eco string // ecosystem tab, empty lists every ecosystem
}

func (PackagesRequest) Validate() error { return nil }

// PackageRow is one row of the /packages listing.
type PackageRow struct {
	Ecosystem, Package               string
	EverBuilt                        bool
	FailingStreak                    int64 // completed attempts since the last success, on any version
	Attempts                         int64
	VersionsAttempted                int64
	VersionsSucceeded                int64
	LastAttemptedAt                  string
	LastAttemptedVersion             string
	LastAttemptedStatus              string
	LastSucceededAt                  string
	LastSucceededVersion             string
	EncodedEcosystem, EncodedPackage string
}

type PackagesData struct {
	Loaded     bool
	AsOf       string
	Eco        string // selected tab, empty lists every ecosystem
	Ecosystems []string
	Total      int64 // packages in the selected scope
	EverBuilt  int64
	NeverBuilt int64
	Failing    int64 // packages whose latest attempts failed
	Listed     int64 // rows shown, at most packagesLimit
	Packages   []PackageRow
}

// encodePackagePath fills the URL-encoded ecosystem and package fields.
func encodePackagePath(eco, pkg string) (string, string) {
	et := packagePathEncoding.Encode(rebuild.Target{Ecosystem: rebuild.Ecosystem(eco), Package: pkg})
	return string(et.Ecosystem), et.Package
}

func PackagesPage(ctx context.Context, req PackagesRequest, deps *Deps) (*PackagesData, error) {
	data := &PackagesData{}
	if deps.Analytics == nil {
		return data, nil
	}
	data.Loaded = true
	data.AsOf = asOf(deps.Analytics)
	err := forEachRow(deps.Analytics, `SELECT DISTINCT ecosystem FROM package_stats ORDER BY 1`,
		func(stmt *sqlite3.Stmt) error {
			data.Ecosystems = append(data.Ecosystems, stmt.ColumnText(0))
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying ecosystems")
	}
	// An unknown tab (stale link) falls back to every ecosystem. The
	// membership check also makes the interpolation below safe.
	where := ``
	if slices.Contains(data.Ecosystems, req.Eco) {
		data.Eco = req.Eco
		where = ` WHERE ecosystem = '` + req.Eco + `'`
	}
	err = forEachRow(deps.Analytics, `
		SELECT count(*), coalesce(sum(ever_built), 0), coalesce(sum(consecutive_failures > 0), 0)
		FROM package_stats`+where,
		func(stmt *sqlite3.Stmt) error {
			data.Total, data.EverBuilt, data.Failing = stmt.ColumnInt64(0), stmt.ColumnInt64(1), stmt.ColumnInt64(2)
			data.NeverBuilt = data.Total - data.EverBuilt
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "counting packages")
	}
	err = forEachRow(deps.Analytics, `
		SELECT ecosystem, package, ever_built, consecutive_failures,
			attempt_count, versions_attempted, versions_succeeded,
			coalesce(last_attempted_time, ''), coalesce(last_attempted_version, ''), coalesce(last_attempted_status, ''),
			coalesce(last_succeeded_time, ''), coalesce(last_succeeded_version, '')
		FROM package_stats`+where+`
		ORDER BY consecutive_failures DESC, attempt_count DESC, ecosystem, package
		LIMIT `+strconv.Itoa(packagesLimit),
		func(stmt *sqlite3.Stmt) error {
			p := PackageRow{
				Ecosystem:            stmt.ColumnText(0),
				Package:              stmt.ColumnText(1),
				EverBuilt:            stmt.ColumnInt64(2) != 0,
				FailingStreak:        stmt.ColumnInt64(3),
				Attempts:             stmt.ColumnInt64(4),
				VersionsAttempted:    stmt.ColumnInt64(5),
				VersionsSucceeded:    stmt.ColumnInt64(6),
				LastAttemptedAt:      stmt.ColumnText(7),
				LastAttemptedVersion: stmt.ColumnText(8),
				LastAttemptedStatus:  stmt.ColumnText(9),
				LastSucceededAt:      stmt.ColumnText(10),
				LastSucceededVersion: stmt.ColumnText(11),
			}
			for _, ts := range []*string{&p.LastAttemptedAt, &p.LastSucceededAt} {
				if len(*ts) >= len("2006-01-02") {
					*ts = (*ts)[:len("2006-01-02")]
				}
			}
			p.EncodedEcosystem, p.EncodedPackage = encodePackagePath(p.Ecosystem, p.Package)
			data.Packages = append(data.Packages, p)
			return nil
		})
	if err != nil {
		return nil, errors.Wrap(err, "querying package stats")
	}
	data.Listed = int64(len(data.Packages))
	return data, nil
}
