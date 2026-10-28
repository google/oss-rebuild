// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestOverviewNoSignals(t *testing.T) {
	a := newMemAnalytics(t,
		`CREATE TABLE signal_coverage (ecosystem TEXT, universe_packages INT, universe_mass REAL, tracked_packages INT, tracked_mass REAL, current_packages INT, current_mass REAL, covered_mass_share REAL, sidecar_built_at TEXT)`,
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

func TestIndexRendersOverview(t *testing.T) {
	var buf bytes.Buffer
	if err := IndexTmpl.Execute(&buf, &IndexData{Overview: coverageOverview}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-chart="columns"`, `data-eco="npm"`, `id="overview-activity"`, `style="flex-grow: 9"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rendered index lacks %s", want)
		}
	}
}
