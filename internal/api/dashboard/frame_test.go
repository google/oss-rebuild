// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadSnapshotFrame(t *testing.T) {
	a := newMemAnalytics(t,
		`CREATE TABLE snapshot_meta (built_at TEXT, watermark TEXT, source_project TEXT, tool_version TEXT)`,
		`INSERT INTO snapshot_meta VALUES ('2026-08-31T23:02:00.000Z', '2026-08-31T23:00:00.000Z', '', '')`,
		`CREATE TABLE version_stats (ecosystem TEXT, package TEXT, version TEXT, first_succeeded_at TEXT)`,
		`INSERT INTO version_stats VALUES
			('npm', 'a', '1', '2026-08-29T10:00:00Z'), ('npm', 'a', '2', '2026-08-30T10:00:00Z'),
			('npm', 'b', '1', '2026-08-30T11:00:00Z'), ('npm', 'c', '1', NULL),
			('pypi', 'a', '1', '2026-08-31T09:00:00Z')`,
		`CREATE TABLE popularity_tiers (ecosystem TEXT, tier INT, packages INT, reproduced INT, not_reproduced INT, retrying INT, in_flight INT, queued INT)`,
		`INSERT INTO popularity_tiers VALUES ('npm', 100, 100, 90, 10, 0, 0, 0), ('npm', 1000, 1000, 900, 100, 0, 0, 0), ('pypi', 1000, 1000, 700, 300, 0, 0, 0)`,
		`CREATE TABLE ecosystem_daily (day TEXT, ecosystem TEXT, attempts INT)`,
		`INSERT INTO ecosystem_daily VALUES ('2026-08-01', 'npm', 99), ('2026-08-31', 'npm', 5), ('2026-09-01', 'npm', 1200), ('2026-09-01', 'pypi', 34)`,
	)
	f, err := loadSnapshotFrame(a)
	if err != nil {
		t.Fatal(err)
	}
	type fig struct{ Figure, Unit string }
	var got []fig
	for _, s := range f.Sections {
		got = append(got, fig{s.Figure, s.Unit})
	}
	want := []fig{{"4", "versions"}, {"80.0%", "top 1k"}, {"3", "packages"}, {"1,234", "builds today"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("figures diff (-want +got):\n%s", diff)
	}
	if !strings.HasPrefix(f.Sections[homeSection].Spark, "M0.0 ") || f.Sections[coverageSection].Bars != true {
		t.Errorf("sparks = %q, %q", f.Sections[homeSection].Spark, f.Sections[coverageSection].Spark)
	}
}

func TestFramedNilPage(t *testing.T) {
	h := Framed(func(context.Context, PackagesRequest, *Deps) (*PackagesData, error) { return nil, nil })
	if o, err := h(context.Background(), PackagesRequest{}, &Deps{}); o != nil || err != nil {
		t.Errorf("Framed = %v, %v, want nil, nil", o, err)
	}
}

func TestLayoutRendersSections(t *testing.T) {
	render := func(f Frame) string {
		var buf bytes.Buffer
		if err := PackagesTmpl.Execute(&buf, &PackagesData{Frame: f}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	bare := render(Frame{})
	for _, want := range []string{`<a href="/coverage"><span class="sec-label">Coverage</span></a>`} {
		if !strings.Contains(bare, want) {
			t.Errorf("frameless page lacks %s", want)
		}
	}
	full := render(Frame{AsOf: "2026-08-31 23:00:00 UTC", Sections: []Section{{Label: "Home", Href: "/", Figure: "4", Unit: "versions", Spark: "M0 1L56 2"}}})
	for _, want := range []string{`<path d="M0 1L56 2"/>`, `<span class="sec-fig">4 <span class="sec-unit">versions</span></span>`, `Snapshot 2026-08-31 23:00:00 UTC`} {
		if !strings.Contains(full, want) {
			t.Errorf("framed page lacks %s", want)
		}
	}
}
