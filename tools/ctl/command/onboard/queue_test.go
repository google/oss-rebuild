// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
)

var testCfg = enqueueConfig{Ecosystem: "npm", MaxVersions: 10,
	FreshnessK: scheduler.DefaultFreshnessK, FreshnessTauHours: scheduler.DefaultFreshnessTauHours}

func versionsOf(cs []scheduler.Campaign) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Version)
	}
	return out
}

func TestValidateSelectors(t *testing.T) {
	base := enqueueConfig{Project: "p", Ecosystem: "npm", SignalsDB: "file:///signals"}
	for _, tc := range []struct {
		name string
		set  func(*enqueueConfig)
		ok   bool
	}{
		{"NamedPackages", func(c *enqueueConfig) { c.FromPackages = "lodash, express" }, true},
		{"HeadWithCap", func(c *enqueueConfig) { c.MaxPackages = 200 }, true},
		{"BoundedHead", func(c *enqueueConfig) { c.FromTop = 1000 }, true},
		{"NoPoolNoCap", func(*enqueueConfig) {}, false},
		{"TwoPools", func(c *enqueueConfig) { c.FromPackages = "lodash"; c.FromTop = 1000 }, false},
		{"NoDatabase", func(c *enqueueConfig) { c.SignalsDB = ""; c.MaxPackages = 200 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.set(&cfg)
			if err := cfg.Validate(); (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.db")
	if _, err := signals.Build(path, []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "b", Prevalence: 0.5},
		{Ecosystem: "npm", Package: "a", Prevalence: 0.9},
		{Ecosystem: "npm", Package: "c", Prevalence: 0.2},
		{Ecosystem: "pypi", Package: "d", Prevalence: 0.8},
	}, nil, signals.Meta{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	sdb, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()
	for _, tc := range []struct {
		name string
		cfg  enqueueConfig
		want []string
	}{
		// Named packages pass through as given, ranked or not.
		{"Named", enqueueConfig{Ecosystem: "npm", FromPackages: "b,zzz"}, []string{"b", "zzz"}},
		// The head comes in score order, bounded by --from-top when set and
		// whole otherwise.
		{"BoundedHead", enqueueConfig{Ecosystem: "npm", FromTop: 2}, []string{"a", "b"}},
		{"WholeHead", enqueueConfig{Ecosystem: "npm", MaxPackages: 5}, []string{"a", "b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.pool(sdb)
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("pool mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenSignalDB(t *testing.T) {
	published := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	built := filepath.Join(t.TempDir(), "signals.db")
	if _, err := signals.Build(built,
		[]signals.PrevalenceRecord{
			{Ecosystem: "npm", Package: "lodash", Prevalence: 0.9},
			{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Prevalence: 1.0, Published: published},
			{Ecosystem: "npm", Package: "other", Version: "1.0.0", Prevalence: 0.5},
		},
		[]signals.ProminenceRecord{
			{Ecosystem: "npm", Package: "lodash", Rating: 0.8, Prominence: 0.6},
		},
		signals.Meta{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	pub := t.TempDir()
	if err := sqlitex.Publish(osfs.New(pub), signals.Object, built); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	sdb, cleanup, err := openSignalDB(context.Background(), "file://"+pub)
	if err != nil {
		t.Fatalf("openSignalDB: %v", err)
	}
	defer cleanup()
	got, err := signals.VersionSignals(sdb, "npm", "lodash")
	if err != nil {
		t.Fatalf("VersionSignals: %v", err)
	}
	want := []signals.VersionSignal{{Version: "4.17.21", Prevalence: 1.0, Published: published}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("VersionSignals mismatch (-want +got):\n%s", diff)
	}
	if sig, ok, err := signals.PackageSignalAt(sdb, "npm", "lodash"); err != nil || !ok || sig.Prominence != 0.6 {
		t.Errorf("PackageSignalAt(lodash) = (%+v, %v, %v), want prominence 0.6", sig, ok, err)
	}
	// A package the export ranks no version of has no rows and no signals.
	if rows, err := signals.VersionSignals(sdb, "npm", "unranked"); err != nil || len(rows) != 0 {
		t.Errorf("VersionSignals(unranked) = (%v, %v), want none", rows, err)
	}
	if sig, ok, err := signals.PackageSignalAt(sdb, "npm", "unranked"); err != nil || ok || sig.Prominence != 0 {
		t.Errorf("PackageSignalAt(unranked) = (%+v, %v, %v), want absent", sig, ok, err)
	}
}

func TestAdmitPrefersPrevalenceOverRecency(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The widely-resolved version is a year old, the fresh one has almost no
	// dependents. Ranking on recency alone would invert these.
	// With no prominence, each score is half its prevalence.
	got := admit(rebuild.NPM, "lodash", 0, []signals.VersionSignal{
		{Version: "4.17.21", Prevalence: 1.0, Published: now.AddDate(-1, 0, 0)},
		{Version: "5.0.0-rc1", Prevalence: 0.01, Published: now.Add(-time.Hour)},
	}, testCfg, now)
	if len(got) != 2 || got[0].Version != "4.17.21" {
		t.Fatalf("admitted = %v, want the widely-depended-upon 4.17.21 first", versionsOf(got))
	}
	if got[0].Score != 0.5 || got[1].Score != 0.005 {
		t.Errorf("scores = %v, %v; want the version prevalences halved to 0.5 and 0.005", got[0].Score, got[1].Score)
	}
	for _, c := range got {
		if c.State != scheduler.StateQueued || c.Stage != scheduler.StageInfer {
			t.Errorf("%s enqueued as %v at %v, want queued at the infer stage", c.Version, c.State, c.Stage)
		}
	}
}

func TestAdmitSharesProminenceAcrossVersions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Every version shares the package's prominence.
	got := admit(rebuild.NPM, "lodash", 0.6, []signals.VersionSignal{
		{Version: "4.17.21", Prevalence: 1.0, Published: now.AddDate(-1, 0, 0)},
		{Version: "3.10.1", Prevalence: 0.2, Published: now.AddDate(-2, 0, 0)},
	}, testCfg, now)
	if len(got) != 2 || got[0].Score != 0.8 || got[1].Score != 0.4 {
		t.Errorf("scores = %v, want 0.8 and 0.4", got)
	}
}

func TestAdmitCapsByDispatchOrder(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := testCfg
	cfg.MaxVersions = 1
	// Equal age, so prevalence decides. Admission uses the same ordering the
	// queue is drained by, so a cap of 1 keeps the right one.
	got := admit(rebuild.NPM, "p", 0, []signals.VersionSignal{
		{Version: "2.0.0", Prevalence: 0.1, Published: now.AddDate(0, -6, 0)},
		{Version: "1.0.0", Prevalence: 0.9, Published: now.AddDate(0, -6, 0)},
	}, cfg, now)
	if len(got) != 1 || got[0].Version != "1.0.0" {
		t.Errorf("admitted = %v, want just 1.0.0", versionsOf(got))
	}
}

func TestAdmitBreaksTiesByRecency(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Equal prevalence and ages far past the freshness horizon leave the
	// dispatch order tied, and the newest must still come first.
	got := admit(rebuild.NPM, "p", 0, []signals.VersionSignal{
		{Version: "1.0.0", Prevalence: 0.5, Published: now.AddDate(-3, 0, 0)},
		{Version: "3.0.0", Prevalence: 0.5, Published: now.AddDate(-1, 0, 0)},
		{Version: "2.0.0", Prevalence: 0.5, Published: now.AddDate(-2, 0, 0)},
	}, testCfg, now)
	if want := []string{"3.0.0", "2.0.0", "1.0.0"}; !cmp.Equal(versionsOf(got), want) {
		t.Errorf("admitted order = %v, want newest first", versionsOf(got))
	}
}

func TestEnqueuePackage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	campaigns := db.NewMemoryCampaigns()
	var errw bytes.Buffer
	cfg := testCfg
	cfg.Ecosystem, cfg.MaxVersions = "cratesio", 2
	rows := []signals.VersionSignal{
		{Version: "0.8.0", Prevalence: 0.4, Published: now.AddDate(-3, 0, 0)},
		{Version: "1.0.0", Prevalence: 0.9, Published: now.AddDate(0, -1, 0)},
		{Version: "0.9.0", Prevalence: 0.5, Published: now.AddDate(-1, 0, 0)},
	}
	if enqueued, skipped := enqueuePackage(ctx, campaigns, &errw, cfg, "serde", 0.5, rows, now); enqueued != 2 || skipped != 0 {
		t.Fatalf("enqueuePackage = (%d, %d), want 2 enqueued", enqueued, skipped)
	}
	// Admission kept the two best-scored versions and derived the crate name
	// without a registry. The rerun sees them as already tracked.
	got, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "cratesio", Package: "serde", Version: "1.0.0", Artifact: "serde-1.0.0.crate"})
	if err != nil || got.Score != 0.7 || got.Published.IsZero() {
		t.Errorf("serde@1.0.0 campaign = (%+v, %v), want scored 0.7 with a publish time", got, err)
	}
	if _, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "cratesio", Package: "serde", Version: "0.8.0", Artifact: "serde-0.8.0.crate"}); err == nil {
		t.Error("serde@0.8.0 was admitted past the cap")
	}
	if enqueued, skipped := enqueuePackage(ctx, campaigns, &errw, cfg, "serde", 0.5, rows, now); enqueued != 0 || skipped != 2 {
		t.Errorf("rerun = (%d, %d), want 2 skipped", enqueued, skipped)
	}
	// A PyPI version the export could not name has no pure wheel. It is
	// skipped before admission, so the cap fills from nameable versions.
	pcfg := testCfg
	pcfg.Ecosystem, pcfg.MaxVersions = "pypi", 1
	prows := []signals.VersionSignal{
		{Version: "2.0.0", Prevalence: 0.9, Published: now},
		{Version: "1.9.0", Prevalence: 0.8, Published: now, Artifact: "numpy-1.9.0-py3-none-any.whl"},
	}
	if enqueued, _ := enqueuePackage(ctx, campaigns, &errw, pcfg, "numpy", 0, prows, now); enqueued != 1 {
		t.Errorf("pypi enqueue = %d, want 1", enqueued)
	}
	if _, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "pypi", Package: "numpy", Version: "1.9.0", Artifact: "numpy-1.9.0-py3-none-any.whl"}); err != nil {
		t.Errorf("numpy@1.9.0: %v, want it admitted in place of the wheel-less 2.0.0", err)
	}
	if !strings.Contains(errw.String(), "skip numpy@2.0.0: no pure wheel") {
		t.Errorf("stderr = %q, want the no-pure-wheel skip", errw.String())
	}
}
