// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOverviewNoSignals(t *testing.T) {
	a := newMemAnalytics(t,
		`CREATE TABLE signal_coverage (ecosystem TEXT, universe_packages INT, universe_mass REAL, tracked_packages INT, tracked_mass REAL, attempted_packages INT, attempted_mass REAL, current_packages INT, current_mass REAL, retrying_packages INT, in_flight_packages INT, queued_packages INT, covered_mass_share REAL, sidecar_built_at TEXT)`,
		`CREATE TABLE package_stats (ecosystem TEXT, package TEXT, ever_built INT)`,
	)
	got, err := loadOverview(a)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("loadOverview() = %+v, want nil", got)
	}
}

func TestOverviewOnly(t *testing.T) {
	o := Overview{Ecos: []EcoBand{{Ecosystem: "npm"}, {Ecosystem: "pypi"}}}
	for eco, want := range map[string][]EcoBand{
		"":      o.Ecos,
		"pypi":  {{Ecosystem: "pypi"}},
		"maven": nil,
	} {
		if diff := cmp.Diff(want, o.Only(eco)); diff != "" {
			t.Errorf("Only(%q) diff (-want +got):\n%s", eco, diff)
		}
	}
}
