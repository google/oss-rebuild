// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

func sibling(id, version, stop string, updated time.Time) schema.AgentSession {
	return schema.AgentSession{
		ID: id, Status: schema.AgentSessionStatusCompleted, StopReason: stop, SuccessIteration: "it-" + id, Updated: updated,
		Target: rebuild.Target{Ecosystem: "npm", Package: "p", Version: version, Artifact: "p-" + version + ".tgz"},
	}
}

func TestNearestSibling(t *testing.T) {
	target := rebuild.Target{Ecosystem: "npm", Package: "p", Version: "1.4.0", Artifact: "p-1.4.0.tgz"}
	success := schema.AgentCompleteReasonSuccess
	for _, tc := range []struct {
		name     string
		sessions []schema.AgentSession
		want     string
	}{
		{name: "None", want: ""},
		{name: "NearestVersion", sessions: []schema.AgentSession{
			sibling("far", "1.0.0", success, testNow), sibling("near", "1.5.0", success, testNow), sibling("mid", "1.2.0", success, testNow),
		}, want: "near"},
		{name: "LatestOfEquallyNear", sessions: []schema.AgentSession{
			sibling("older", "1.3.0", success, testNow.Add(-time.Hour)), sibling("newer", "1.3.0", success, testNow),
		}, want: "newer"},
		{name: "Skipped", sessions: []schema.AgentSession{
			sibling("same-version", "1.4.0", success, testNow),
			sibling("failed", "1.3.0", schema.AgentCompleteReasonFailed, testNow),
			sibling("gave-up", "1.3.0", schema.AgentCompleteReasonGaveUp, testNow),
			editSession(sibling("running", "1.3.0", success, testNow), func(s *schema.AgentSession) { s.Status = schema.AgentSessionStatusRunning }),
			editSession(sibling("unconfirmed", "1.3.0", success, testNow), func(s *schema.AgentSession) { s.SuccessIteration = "" }),
			editSession(sibling("other-artifact", "1.3.0", success, testNow), func(s *schema.AgentSession) { s.Target.Artifact = "p-1.3.0.zip" }),
		}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nearestSibling(target, tc.sessions)
			var id string
			if got != nil {
				id = got.ID
			}
			if id != tc.want {
				t.Errorf("nearestSibling = %q, want %q", id, tc.want)
			}
		})
	}
}

func editSession(s schema.AgentSession, edit func(*schema.AgentSession)) schema.AgentSession {
	edit(&s)
	return s
}

func TestPassSeedsAgentLaunches(t *testing.T) {
	ctx := context.Background()
	strategy := &schema.StrategyOneOf{ManualStrategy: &rebuild.ManualStrategy{Build: "npm pack"}}
	f := &fakeAPI{}
	d := newTestDispatcher(Config{Batch: 10, MaxAgent: 5, AgentIterations: 5, MaxRetries: 3, InflightTimeout: time.Hour, Workers: 1}, f)
	d.Iterations = db.NewMemoryIterations()
	c := queued("p", scheduler.StageAgent)
	d.Campaigns.Upsert(ctx, c)
	s := sibling("sib", "0.9.0", schema.AgentCompleteReasonSuccess, testNow)
	s.Target.Package, s.Target.Artifact = c.Package, c.Package+"-0.9.0.tgz"
	d.Sessions.Insert(ctx, s)
	d.Iterations.Insert(ctx, schema.AgentIteration{ID: s.SuccessIteration, SessionID: s.ID, Strategy: strategy})
	d.Pass(ctx, []scheduler.Campaign{c}, new(scheduler.Budget))
	if len(f.agents) != 1 {
		t.Fatalf("agent launches = %d, want 1", len(f.agents))
	}
	req := f.agents[0]
	if req.Context == nil || req.Context.Seed == nil {
		t.Fatalf("launch carries no seed")
	}
	if seed := req.Context.Seed; seed.Session != "sib" || seed.Version != "0.9.0" || seed.Strategy == nil {
		t.Errorf("seed = %+v", seed)
	}
	if req.MaxIterations != 6 {
		t.Errorf("max iterations = %d, want 6 with the seed's build", req.MaxIterations)
	}
}
