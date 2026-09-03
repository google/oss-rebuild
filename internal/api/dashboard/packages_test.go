// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func packagesFixture(t *testing.T) memAnalytics {
	return newMemAnalytics(t,
		`CREATE TABLE package_stats (ecosystem TEXT, package TEXT, ever_built INT, consecutive_failures INT, attempt_count INT, versions_attempted INT, versions_succeeded INT, last_attempted_time TEXT, last_attempted_version TEXT, last_attempted_status TEXT, last_succeeded_time TEXT, last_succeeded_version TEXT)`,
		`INSERT INTO package_stats VALUES
			('npm', 'a', 1, 0, 2, 2, 2, '2026-08-30T10:00:00Z', '1.1', 'SUCCESS', '2026-08-30T10:00:00Z', '1.1'),
			('npm', 'b', 0, 3, 3, 1, 0, '2026-08-31T10:00:00Z', '2.0', 'FAILURE', NULL, NULL),
			('pypi', 'c', 1, 1, 4, 2, 1, '2026-08-29T10:00:00Z', '0.2', 'ERROR', '2026-08-28T10:00:00Z', '0.1')`,
	)
}

func TestPackagesPage(t *testing.T) {
	a := PackageRow{Ecosystem: "npm", Package: "a", EverBuilt: true, Attempts: 2, VersionsAttempted: 2, VersionsSucceeded: 2, LastAttemptedAt: "2026-08-30", LastAttemptedVersion: "1.1", LastAttemptedStatus: "SUCCESS", LastSucceededAt: "2026-08-30", LastSucceededVersion: "1.1"}
	b := PackageRow{Ecosystem: "npm", Package: "b", FailingStreak: 3, Attempts: 3, VersionsAttempted: 1, LastAttemptedAt: "2026-08-31", LastAttemptedVersion: "2.0", LastAttemptedStatus: "FAILURE"}
	c := PackageRow{Ecosystem: "pypi", Package: "c", EverBuilt: true, FailingStreak: 1, Attempts: 4, VersionsAttempted: 2, VersionsSucceeded: 1, LastAttemptedAt: "2026-08-29", LastAttemptedVersion: "0.2", LastAttemptedStatus: "ERROR", LastSucceededAt: "2026-08-28", LastSucceededVersion: "0.1"}
	for _, tc := range []struct {
		name string
		req  PackagesRequest
		want PackagesData
	}{
		{
			name: "LongestStreakFirst",
			want: PackagesData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecosystems: []string{"npm", "pypi"}, Total: 3, EverBuilt: 2, NeverBuilt: 1, Failing: 2, Listed: 3, Packages: []PackageRow{b, c, a}},
		},
		{
			name: "EcosystemTab",
			req:  PackagesRequest{Eco: "npm"},
			want: PackagesData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Eco: "npm", Ecosystems: []string{"npm", "pypi"}, Total: 2, EverBuilt: 1, NeverBuilt: 1, Failing: 1, Listed: 2, Packages: []PackageRow{b, a}},
		},
		{
			name: "UnknownTabFallsBack",
			req:  PackagesRequest{Eco: "cratesio"},
			want: PackagesData{Loaded: true, AsOf: "2026-09-01 00:00:00 UTC", Ecosystems: []string{"npm", "pypi"}, Total: 3, EverBuilt: 2, NeverBuilt: 1, Failing: 2, Listed: 3, Packages: []PackageRow{b, c, a}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PackagesPage(context.Background(), tc.req, &Deps{Analytics: packagesFixture(t)})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(&tc.want, got, cmpopts.IgnoreFields(PackageRow{}, "EncodedEcosystem", "EncodedPackage")); diff != "" {
				t.Errorf("PackagesPage diff (-want +got):\n%s", diff)
			}
			if err := PackagesTmpl.Execute(io.Discard, got); err != nil {
				t.Errorf("rendering: %v", err)
			}
		})
	}
}

func TestPackagesPageNoAnalytics(t *testing.T) {
	got, err := PackagesPage(context.Background(), PackagesRequest{}, &Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(&PackagesData{}, got); diff != "" {
		t.Errorf("PackagesPage diff (-want +got):\n%s", diff)
	}
}
