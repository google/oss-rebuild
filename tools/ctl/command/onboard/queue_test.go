// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
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

func TestRankedVersions(t *testing.T) {
	published := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := jsonl.Encode(&buf, []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "lodash", Prevalence: 0.9},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Prevalence: 1.0, Published: published},
		{Ecosystem: "npm", Package: "other", Version: "1.0.0", Prevalence: 0.5},
		{Ecosystem: "pypi", Package: "lodash", Version: "4.17.21", Prevalence: 0.4, Artifact: "lodash-4.17.21-py3-none-any.whl"},
	}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := rankedVersions(jsonl.Decode[signals.PrevalenceRecord](&buf), "npm", []string{"lodash", "missing"})
	if err != nil {
		t.Fatalf("rankedVersions: %v", err)
	}
	// Package rows, other packages, and other ecosystems are left out, and a
	// package the export ranks no version of has no entry.
	want := map[string][]signals.VersionSignal{"lodash": {{Version: "4.17.21", Prevalence: 1.0, Published: published}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("rankedVersions mismatch (-want +got):\n%s", diff)
	}
}

func TestAdmitPrefersPrevalenceOverRecency(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The widely-resolved version is a year old, the fresh one has almost no
	// dependents. Ranking on recency alone would invert these.
	got := admit(rebuild.NPM, "lodash", []signals.VersionSignal{
		{Version: "4.17.21", Prevalence: 1.0, Published: now.AddDate(-1, 0, 0)},
		{Version: "5.0.0-rc1", Prevalence: 0.01, Published: now.Add(-time.Hour)},
	}, testCfg, now)
	if len(got) != 2 || got[0].Version != "4.17.21" {
		t.Fatalf("admitted = %v, want the widely-depended-upon 4.17.21 first", versionsOf(got))
	}
	if got[0].Score != 1.0 || got[1].Score != 0.01 {
		t.Errorf("scores = %v, %v; want the version prevalences 1 and 0.01", got[0].Score, got[1].Score)
	}
	for _, c := range got {
		if c.State != scheduler.StateQueued || c.Stage != scheduler.StageInfer {
			t.Errorf("%s enqueued as %v at %v, want queued at the infer stage", c.Version, c.State, c.Stage)
		}
	}
}

func TestAdmitCapsByDispatchOrder(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := testCfg
	cfg.MaxVersions = 1
	// Equal age, so prevalence decides. Admission uses the same ordering the
	// queue is drained by, so a cap of 1 keeps the right one.
	got := admit(rebuild.NPM, "p", []signals.VersionSignal{
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
	got := admit(rebuild.NPM, "p", []signals.VersionSignal{
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
	if enqueued, skipped := enqueuePackage(ctx, campaigns, &errw, cfg, "serde", rows, now); enqueued != 2 || skipped != 0 {
		t.Fatalf("enqueuePackage = (%d, %d), want 2 enqueued", enqueued, skipped)
	}
	// Admission kept the two best-scored versions and derived the crate name
	// without a registry. The rerun sees them as already tracked.
	got, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "cratesio", Package: "serde", Version: "1.0.0", Artifact: "serde-1.0.0.crate"})
	if err != nil || got.Score != 0.9 || got.Published.IsZero() {
		t.Errorf("serde@1.0.0 campaign = (%+v, %v), want scored 0.9 with a publish time", got, err)
	}
	if _, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "cratesio", Package: "serde", Version: "0.8.0", Artifact: "serde-0.8.0.crate"}); err == nil {
		t.Error("serde@0.8.0 was admitted past the cap")
	}
	if enqueued, skipped := enqueuePackage(ctx, campaigns, &errw, cfg, "serde", rows, now); enqueued != 0 || skipped != 2 {
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
	if enqueued, _ := enqueuePackage(ctx, campaigns, &errw, pcfg, "numpy", prows, now); enqueued != 1 {
		t.Errorf("pypi enqueue = %d, want 1", enqueued)
	}
	if _, err := campaigns.Get(ctx, rebuild.Target{Ecosystem: "pypi", Package: "numpy", Version: "1.9.0", Artifact: "numpy-1.9.0-py3-none-any.whl"}); err != nil {
		t.Errorf("numpy@1.9.0: %v, want it admitted in place of the wheel-less 2.0.0", err)
	}
	if !strings.Contains(errw.String(), "skip numpy@2.0.0: no pure wheel") {
		t.Errorf("stderr = %q, want the no-pure-wheel skip", errw.String())
	}
}
