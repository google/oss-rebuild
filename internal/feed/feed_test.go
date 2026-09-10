// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
)

func TestPrerelease(t *testing.T) {
	for _, tc := range []struct {
		eco     rebuild.Ecosystem
		version string
		want    bool
	}{
		{rebuild.NPM, "1.2.3", false},
		{rebuild.NPM, "1.2.3-canary.4", true},
		{rebuild.NPM, "latest", true},
		{rebuild.CratesIO, "0.3.1", false},
		{rebuild.CratesIO, "1.0.0-beta.2", true},
		{rebuild.PyPI, "7.31.0", false},
		{rebuild.PyPI, "1.0.post1", false},
		{rebuild.PyPI, "1.0cu121", false},
		{rebuild.PyPI, "1.0rc1", true},
		{rebuild.PyPI, "1.0a1", true},
		{rebuild.PyPI, "1.0.0-alpha", true},
		{rebuild.PyPI, "2.46.2.dev10", true},
		{rebuild.PyPI, "0.6.0.dev20260728", true},
		{rebuild.PyPI, "1.0dev", true},
		{rebuild.RubyGems, "3.1.0", false},
		{rebuild.RubyGems, "3.1.0.rc1", true},
	} {
		if got := prerelease(tc.eco, tc.version); got != tc.want {
			t.Errorf("prerelease(%s, %q) = %v, want %v", tc.eco, tc.version, got, tc.want)
		}
	}
}

func TestRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.db")
	if _, err := signals.Build(path, []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "lodash", Prevalence: 1.0},
		{Ecosystem: "cratesio", Package: "serde", Prevalence: 0.8},
	}, []signals.ProminenceRecord{
		{Ecosystem: "npm", Package: "lodash", Rating: 0.8, Prominence: 0.6},
	}, signals.Meta{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	sdb, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	campaigns := db.NewMemoryCampaigns()
	p := &Pass{Campaigns: campaigns, Signals: sdb, Config: Defaults, Err: io.Discard, Now: now}
	events := []Event{
		{Ecosystem: "npm", Package: "lodash", Version: "5.0.0", Published: now.Add(-time.Hour)},
		{Ecosystem: "npm", Package: "lodash", Version: "5.0.1", Published: now.Add(-time.Minute)},
		{Ecosystem: "npm", Package: "lodash", Version: "6.0.0-alpha.1", Published: now},
		{Ecosystem: "npm", Package: "left-pad", Version: "1.3.0", Published: now},
		{Ecosystem: "cratesio", Package: "serde", Version: "1.0.200"},
		{Ecosystem: "pypi", Package: "requests", Version: "2.33.0", Published: now},
	}
	got, err := p.Run(context.Background(), events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := &Summary{Events: 6, Prerelease: 1, Superseded: 1, Untracked: 2, Admitted: 2}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Summary mismatch (-want +got):\n%s", diff)
	}
	// The newest stable lodash release is the one admitted.
	c, err := campaigns.Get(context.Background(), rebuild.Target{Ecosystem: rebuild.NPM, Package: "lodash", Version: "5.0.1", Artifact: "lodash-5.0.1.tgz"})
	if err != nil {
		t.Fatalf("Get(lodash@5.0.1): %v", err)
	}
	wantC := scheduler.Campaign{
		Ecosystem: "npm", Package: "lodash", Version: "5.0.1", Artifact: "lodash-5.0.1.tgz",
		Stage: scheduler.StageInfer, State: scheduler.StateQueued,
		Score: signals.Score(1.0, 0.6), Published: now.Add(-time.Minute), Origin: scheduler.OriginFeed,
		NotBefore: now.Add(-time.Minute).Add(Defaults.Settle), Updated: now,
	}
	if diff := cmp.Diff(wantC, c); diff != "" {
		t.Errorf("campaign mismatch (-want +got):\n%s", diff)
	}
	// An event without a publish time is published as of the pass.
	if c, err := campaigns.Get(context.Background(), rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "serde", Version: "1.0.200", Artifact: "serde-1.0.200.crate"}); err != nil || !c.Published.Equal(now) {
		t.Errorf("Get(serde) = (%+v, %v), want published at now", c, err)
	}
	// A replayed batch admits nothing new.
	got, err = p.Run(context.Background(), events)
	if err != nil {
		t.Fatalf("Run again: %v", err)
	}
	if got.Admitted != 0 || got.Tracked != 2 {
		t.Errorf("replay: admitted=%d tracked=%d, want 0 and 2", got.Admitted, got.Tracked)
	}
}
