// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func vsRebuild(version string, status schema.RebuildStatus, msg string, created time.Time) rundex.Rebuild {
	return rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{
		Ecosystem: "npm", Package: "p", Version: version, RunID: created.Format(time.RFC3339),
		Status: status, Success: status == schema.RebuildStatusSuccess, Message: msg, Created: created,
	}}
}

func sess(version, stop, summary string, created time.Time) schema.AgentSession {
	s := schema.AgentSession{ID: created.Format(time.RFC3339), Target: rebuild.Target{Ecosystem: "npm", Package: "p", Version: version}, Created: created, Summary: summary}
	if stop == "" {
		s.Status = schema.AgentSessionStatusRunning
	} else {
		s.Status, s.StopReason = schema.AgentSessionStatusCompleted, stop
	}
	return s
}

// events builds the package page's events from canned attempts and sessions.
func events(rebuilds []rundex.Rebuild, sessions []schema.AgentSession) []PackageEvent {
	var evs []PackageEvent
	for _, rb := range rebuilds {
		evs = append(evs, rebuildEvent(rb))
	}
	for _, s := range sessions {
		evs = append(evs, sessionEvent(s))
	}
	return evs
}

func TestComputeVersionStatuses(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hour := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	published := []string{"4.0", "3.0", "2.0", "1.0", "0.9"}
	got, untried := computeVersionStatuses("npm", "p", published, events(
		[]rundex.Rebuild{
			vsRebuild("3.0", schema.RebuildStatusError, "getting strategy: no git repo", hour(0)),
			vsRebuild("3.0", schema.RebuildStatusSuccess, "", hour(1)),
			vsRebuild("2.0", schema.RebuildStatusError, "executing rebuild: rpc error: code = FailedPrecondition desc = rebuild content mismatch", hour(2)),
			vsRebuild("1.0", schema.RebuildStatusRunning, "", hour(0)),
			// Attempted but not published, so it is listed without counting
			// against untried. It sorts below 0.9 only as a string.
			vsRebuild("0.10", schema.RebuildStatusSuccess, "", hour(0)),
		},
		[]schema.AgentSession{
			sess("2.0", schema.AgentCompleteReasonFailed, "Maximum iterations (5) reached", hour(1)),
			sess("0.9", schema.AgentCompleteReasonThrottled, "Session throttled: 1 transient LLM failures", hour(0)),
		}))
	if untried != 1 {
		t.Errorf("untried = %d, want 1", untried)
	}
	var order []string
	byVer := map[string]VersionStatus{}
	for _, v := range got {
		order = append(order, v.Version)
		byVer[v.Version] = v
	}
	if want := []string{"3.0", "2.0", "1.0", "0.10", "0.9"}; !reflect.DeepEqual(order, want) {
		t.Errorf("versions = %v, want %v", order, want)
	}
	for v, want := range map[string]VersionState{"3.0": StateVerified, "2.0": StateFailed, "1.0": StateRunning, "0.10": StateVerified, "0.9": StateFailed} {
		if byVer[v].State != want {
			t.Errorf("%s: state %q, want %q", v, byVer[v].State, want)
		}
	}
	// A failure before the version reproduced is history.
	if v := byVer["3.0"]; v.Failure == nil || v.Failure.Stage != "infer" || !v.Fixed {
		t.Errorf("3.0: failure %+v fixed %v, want the infer failure, fixed", v.Failure, v.Fixed)
	}
	// Events run oldest first across kinds, and the failure is the latest.
	v := byVer["2.0"]
	if len(v.Events) != 2 || !v.Events[0].Agent || v.Events[1].Agent {
		t.Errorf("2.0: events %+v, want the session then the rebuild", v.Events)
	}
	if v.Failure == nil || v.Failure.Stage != "compare" || v.Failure.Cause != "rebuild content mismatch" || v.Fixed {
		t.Errorf("2.0: failure %+v fixed %v, want the compare failure, not fixed", v.Failure, v.Fixed)
	}
	if !v.Last().Equal(hour(2)) {
		t.Errorf("2.0: last tried %v, want %v", v.Last(), hour(2))
	}
	if f := byVer["0.9"].Failure; f == nil || f.Stage != "agent" || f.Cause != "Session throttled: 1 transient LLM failures" {
		t.Errorf("0.9: failure %+v, want the throttled session", f)
	}
}

func TestSummarize(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hour := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	statuses, _ := computeVersionStatuses("npm", "p", nil, events(
		[]rundex.Rebuild{
			vsRebuild("1.0", schema.RebuildStatusError, "getting strategy: no git repo", hour(0)),
			vsRebuild("1.0", schema.RebuildStatusError, "getting strategy: no git repo", hour(2)),
			vsRebuild("2.0", schema.RebuildStatusError, "getting strategy: no git repo", hour(5)),
		},
		[]schema.AgentSession{
			sess("1.0", schema.AgentCompleteReasonSuccess, "Build successful", hour(1)),
			sess("1.0", schema.AgentCompleteReasonGaveUp, "Gave up: The sources are not public.", hour(3)),
			sess("2.0", "", "", hour(4)),
		}))
	s := summarize(statuses)
	want := PackageSummary{
		Verified: 1, Attempted: 2,
		Rebuilds: 3, Sessions: 3,
		FailedRebuilds: 3, FailedSessions: 1,
		StreakRebuilds: 2, StreakSessions: 1,
		Since: hour(1),
	}
	got := s
	got.LastOK, got.Failure = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summarize() = %+v, want %+v", got, want)
	}
	if s.LastOK == nil || !s.LastOK.Agent || s.LastOK.Version != "1.0" {
		t.Errorf("LastOK = %+v, want the 1.0 session", s.LastOK)
	}
	if s.Failure == nil || s.Failure.Version != "2.0" || s.Attempts() != 6 {
		t.Errorf("Failure = %+v, Attempts() = %d, want the 2.0 rebuild of 6", s.Failure, s.Attempts())
	}

	// With nothing reproduced the streak starts at the first failure.
	statuses, _ = computeVersionStatuses("npm", "p", nil, events(
		[]rundex.Rebuild{vsRebuild("1.0", schema.RebuildStatusError, "", hour(2))},
		[]schema.AgentSession{sess("1.0", schema.AgentCompleteReasonError, "", hour(3))}))
	if s := summarize(statuses); s.LastOK != nil || !s.Since.Equal(hour(2)) || s.StreakRebuilds != 1 || s.StreakSessions != 1 {
		t.Errorf("summarize() = %+v, want a streak of one each since the first failure", s)
	}
}

func TestFailureCauses(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hour := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	timeout := "getting strategy: fetching inference: rpc error: code = Internal desc = failed to infer strategy: fetching metadata: dial tcp %s:443: i/o timeout"
	statuses, _ := computeVersionStatuses("npm", "p", nil, events(
		[]rundex.Rebuild{
			// The same failure through different addresses shares a cause.
			vsRebuild("2.0", schema.RebuildStatusError, fmt.Sprintf(timeout, "10.0.0.1"), hour(0)),
			vsRebuild("2.0", schema.RebuildStatusError, fmt.Sprintf(timeout, "10.0.0.2"), hour(1)),
			vsRebuild("1.0", schema.RebuildStatusError, "getting strategy: no git repo", hour(0)),
			vsRebuild("1.0", schema.RebuildStatusSuccess, "", hour(4)),
		},
		[]schema.AgentSession{
			sess("2.0", schema.AgentCompleteReasonFailed, "Maximum iterations (5) reached", hour(2)),
			sess("1.0", schema.AgentCompleteReasonFailed, "Maximum iterations (5) reached", hour(3)),
		}))
	causes, more := failureCauses(statuses)
	type row struct {
		stage, cause, versions string
		n, pct                 int
		first                  time.Time
	}
	var got []row
	for _, c := range causes {
		got = append(got, row{c.Latest.Stage, c.Latest.Cause, c.Versions, c.N, c.Pct, c.First})
	}
	want := []row{
		{"agent", "Maximum iterations (5) reached", "all 2 versions", 2, 100, hour(2)},
		{"infer", "fetching metadata: dial tcp 10.0.0.2:443: i/o timeout", "2.0", 2, 100, hour(0)},
		{"infer", "no git repo", "1.0", 1, 50, hour(0)},
	}
	if !reflect.DeepEqual(got, want) || more != 0 {
		t.Errorf("failureCauses() = %+v, %d, want %+v, 0", got, more, want)
	}

	var rebuilds []rundex.Rebuild
	for i := range causesShown + 2 {
		rebuilds = append(rebuilds, vsRebuild(fmt.Sprintf("%d.0", i), schema.RebuildStatusError, fmt.Sprintf("cause %d", i), hour(i)))
	}
	statuses, _ = computeVersionStatuses("npm", "p", nil, events(rebuilds, nil))
	if causes, more := failureCauses(statuses); len(causes) != causesShown || more != 2 {
		t.Errorf("failureCauses() kept %d and left %d, want %d and 2", len(causes), more, causesShown)
	}
}

func TestPackageEventCause(t *testing.T) {
	for _, tc := range []struct {
		name              string
		event             PackageEvent
		context, headline string
	}{
		{"Split", sessionEvent(sess("1", schema.AgentCompleteReasonFailed, "Inference failed: generating strategy: clone failed [repo=https://x.org/y]: Unsupported repo URI\n", time.Time{})), "clone failed [repo=https://x.org/y]", "Unsupported repo URI"},
		{"Whole", rebuildEvent(vsRebuild("1", schema.RebuildStatusError, "getting strategy: no git repo", time.Time{})), "", "no git repo"},
		{"GaveUp", sessionEvent(sess("1", schema.AgentCompleteReasonGaveUp, "Gave up: The tag is missing: 1.0 was never pushed.", time.Time{})), "", "The tag is missing: 1.0 was never pushed."},
		{"NoMessage", rebuildEvent(vsRebuild("1", schema.RebuildStatusCancelled, "", time.Time{})), "", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.event.Context(); got != tc.context {
				t.Errorf("Context() = %q, want %q", got, tc.context)
			}
			if got := tc.event.Headline(); got != tc.headline {
				t.Errorf("Headline() = %q, want %q", got, tc.headline)
			}
		})
	}
}
