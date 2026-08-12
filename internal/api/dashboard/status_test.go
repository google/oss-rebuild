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
	rebuilds := []rundex.Rebuild{
		vsRebuild("3.0", schema.RebuildStatusSuccess, true, t0),
		vsRebuild("2.0", schema.RebuildStatusError, false, t0),
		vsRebuild("1.0", schema.RebuildStatusRunning, false, t0),
	}
	got := statusByVersion(computeVersionStatuses("npm", "p", rebuilds, nil))

	if len(got) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(got))
	}
	checks := map[string]VersionState{"3.0": StateVerified, "2.0": StateFailed, "1.0": StateRunning}
	for v, want := range checks {
		if got[v].State != want {
			t.Errorf("version %s: got cell %q, want %q", v, got[v].State, want)
		}
	}
}

func TestComputeVersionStatuses_Agent(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessions := []schema.AgentSession{
		sess("3.0", "success", t0),
		sess("2.0", "running", t0),
		sess("1.0", "failed", t0),
	}
	got := statusByVersion(computeVersionStatuses("npm", "p", nil, sessions))
	if got["3.0"].State != StateVerified || got["3.0"].Agent != AgentFixed {
		t.Errorf("3.0: cell=%q agent=%q, want verified/fixed", got["3.0"].State, got["3.0"].Agent)
	}
	if got["2.0"].State != StateRunning || got["2.0"].Agent != AgentRunning {
		t.Errorf("2.0: cell=%q agent=%q, want running/running", got["2.0"].State, got["2.0"].Agent)
	}
	if got["1.0"].State != StateFailed || got["1.0"].Agent != AgentFailed {
		t.Errorf("1.0: cell=%q agent=%q, want failed/failed", got["1.0"].State, got["1.0"].Agent)
	}
}

func TestComputeVersionStatuses_RecencyOrder(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rebuilds := []rundex.Rebuild{
		vsRebuild("1.0", schema.RebuildStatusSuccess, true, t0),
		vsRebuild("2.0", schema.RebuildStatusSuccess, true, t0.Add(time.Hour)),
	}
	got := computeVersionStatuses("debian", "p", rebuilds, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 attempted versions, got %d", len(got))
	}
	if got[0].Version != "2.0" {
		t.Errorf("expected most-recent version first, got %q", got[0].Version)
	}
}

func TestSummarize(t *testing.T) {
	statuses := []VersionStatus{
		{Version: "4.0", State: StateVerified, Attempts: 1},
		{Version: "3.0", State: StateFailed, Attempts: 2},
		{Version: "2.0", State: StateRunning, Sessions: 1},
	}
	s := summarize(statuses)
	if s.Attempted != 3 || s.Verified != 1 || s.Failed != 1 || s.Running != 1 {
		t.Errorf("unexpected summary counts: %+v", s)
	}
	if s.VerifiedPct != 33 { // 1 verified / 3 attempted
		t.Errorf("VerifiedPct = %d, want 33", s.VerifiedPct)
	}
	if s.AgentRuns != 1 {
		t.Errorf("AgentRuns = %d, want 1", s.AgentRuns)
	}
}
