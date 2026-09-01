// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func vsRebuild(version string, status schema.RebuildStatus, success bool, created time.Time) rundex.Rebuild {
	return rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{
		Ecosystem: "npm", Package: "p", Version: version,
		Status: status, Success: success, Created: created,
	}}
}

func sess(version, statusOrStop string, created time.Time) schema.AgentSession {
	s := schema.AgentSession{Target: rebuild.Target{Ecosystem: "npm", Package: "p", Version: version}, Created: created}
	switch statusOrStop {
	case "running":
		s.Status = schema.AgentSessionStatusRunning
	case "success":
		s.Status = schema.AgentSessionStatusCompleted
		s.StopReason = schema.AgentCompleteReasonSuccess
	case "failed":
		s.Status = schema.AgentSessionStatusCompleted
		s.StopReason = schema.AgentCompleteReasonFailed
	case "throttled":
		s.Status = schema.AgentSessionStatusCompleted
		s.StopReason = schema.AgentCompleteReasonThrottled
	}
	return s
}

func statusByVersion(list []VersionStatus) map[string]VersionStatus {
	m := make(map[string]VersionStatus, len(list))
	for _, v := range list {
		m[v.Version] = v
	}
	return m
}

func TestComputeVersionStatuses_Classification(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	axis := []string{"4.0", "3.0", "2.0", "1.0"} // newest-first; "4.0" is untried
	rebuilds := []rundex.Rebuild{
		vsRebuild("3.0", schema.RebuildStatusSuccess, true, t0),
		vsRebuild("2.0", schema.RebuildStatusError, false, t0),
		vsRebuild("1.0", schema.RebuildStatusRunning, false, t0),
	}
	got := statusByVersion(computeVersionStatuses("npm", "p", axis, true, rebuilds, nil))

	if len(got) != 4 {
		t.Fatalf("expected 4 versions, got %d", len(got))
	}
	checks := map[string]VersionState{"4.0": StateUntried, "3.0": StateVerified, "2.0": StateFailed, "1.0": StateRunning}
	for v, want := range checks {
		if got[v].State != want {
			t.Errorf("version %s: got cell %q, want %q", v, got[v].State, want)
		}
	}
}

func TestComputeVersionStatuses_Agent(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	axis := []string{"3.0", "2.0", "1.0", "0.9"}
	sessions := []schema.AgentSession{
		sess("3.0", "success", t0),
		sess("2.0", "running", t0),
		sess("1.0", "failed", t0),
		sess("0.9", "throttled", t0),
	}
	got := statusByVersion(computeVersionStatuses("npm", "p", axis, true, nil, sessions))
	if got["3.0"].State != StateVerified || got["3.0"].Agent != AgentFixed {
		t.Errorf("3.0: cell=%q agent=%q, want verified/fixed", got["3.0"].State, got["3.0"].Agent)
	}
	if got["2.0"].State != StateRunning || got["2.0"].Agent != AgentRunning {
		t.Errorf("2.0: cell=%q agent=%q, want running/running", got["2.0"].State, got["2.0"].Agent)
	}
	if got["1.0"].State != StateFailed || got["1.0"].Agent != AgentFailed {
		t.Errorf("1.0: cell=%q agent=%q, want failed/failed", got["1.0"].State, got["1.0"].Agent)
	}
	// Throttled is neither a failure nor an absence of agent activity.
	if got["0.9"].State != StateUntried || got["0.9"].Agent != AgentThrottled {
		t.Errorf("0.9: cell=%q agent=%q, want untried/throttled", got["0.9"].State, got["0.9"].Agent)
	}
}

func TestComputeVersionStatuses_Regression(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Newest-first: 3.0 & 2.0 fail, 1.0 verified => 2.0 and 3.0 are regressions.
	axis := []string{"3.0", "2.0", "1.0"}
	rebuilds := []rundex.Rebuild{
		vsRebuild("3.0", schema.RebuildStatusError, false, t0),
		vsRebuild("2.0", schema.RebuildStatusError, false, t0),
		vsRebuild("1.0", schema.RebuildStatusSuccess, true, t0),
	}
	got := statusByVersion(computeVersionStatuses("npm", "p", axis, true, rebuilds, nil))
	if !got["3.0"].Regressed || !got["2.0"].Regressed {
		t.Errorf("expected 3.0 and 2.0 flagged as regressions: 3.0=%v 2.0=%v", got["3.0"].Regressed, got["2.0"].Regressed)
	}
	if got["1.0"].Regressed {
		t.Error("1.0 (verified) should not be a regression")
	}

	// No older verified version => no regression.
	rebuilds2 := []rundex.Rebuild{
		vsRebuild("3.0", schema.RebuildStatusError, false, t0),
		vsRebuild("2.0", schema.RebuildStatusError, false, t0),
	}
	got2 := statusByVersion(computeVersionStatuses("npm", "p", axis, true, rebuilds2, nil))
	if got2["3.0"].Regressed || got2["2.0"].Regressed {
		t.Error("no verified older version: nothing should be flagged as a regression")
	}
}

func TestComputeVersionStatuses_VersionOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// supported=false ignores the axis and orders attempted versions newest
	// first. 0.10 was attempted before 0.9 and sorts below it as a string,
	// so neither recency nor lexicographic order would rank it first.
	rebuilds := []rundex.Rebuild{
		vsRebuild("0.10", schema.RebuildStatusSuccess, true, t0),
		vsRebuild("0.9", schema.RebuildStatusSuccess, true, t0.Add(time.Hour)),
	}
	got := computeVersionStatuses("debian", "p", []string{"ignored"}, false, rebuilds, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 attempted versions, got %d", len(got))
	}
	if got[0].Version != "0.10" {
		t.Errorf("expected newest version first, got %q", got[0].Version)
	}
	for _, v := range got {
		if v.Regressed {
			t.Errorf("regression should not be computed on unsupported (unordered) axis: %+v", v)
		}
	}
}

func TestSummarize(t *testing.T) {
	statuses := []VersionStatus{
		{Version: "4.0", State: StateVerified, Attempts: 1},
		{Version: "3.0", State: StateFailed, Attempts: 2, Regressed: true},
		{Version: "2.0", State: StateRunning, Sessions: 1},
		{Version: "1.0", State: StateUntried},
	}
	s := summarize(statuses, true)
	if s.TotalVersions != 4 || s.Attempted != 3 || s.Verified != 1 || s.Failed != 1 || s.Running != 1 {
		t.Errorf("unexpected summary counts: %+v", s)
	}
	if s.NeedsAttention != 1 {
		t.Errorf("NeedsAttention = %d, want 1", s.NeedsAttention)
	}
	if s.VerifiedPct != 25 { // 1 verified / 4 total versions
		t.Errorf("VerifiedPct = %d, want 25", s.VerifiedPct)
	}
	if s.AgentRuns != 1 {
		t.Errorf("AgentRuns = %d, want 1", s.AgentRuns)
	}
}
