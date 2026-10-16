// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

var (
	testNow   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testRunID = testNow.Format(time.RFC3339Nano)
)

// fakeAPI records the launches a pass makes.
type fakeAPI struct {
	creates   []schema.RebuildPackageRequest
	agents    []schema.AgentCreateRequest
	createErr error
}

func newTestDispatcher(cfg dispatchConfig, f *fakeAPI) *dispatcher {
	return &dispatcher{
		io:        cli.IO{Out: io.Discard, Err: io.Discard},
		campaigns: db.NewMemoryCampaigns(),
		budgets:   db.NewMemoryBudgets(),
		attempts:  db.NewMemoryAttempts(),
		sessions:  db.NewMemorySessions(),
		create: func(ctx context.Context, r schema.RebuildPackageRequest) (*longrunning.Operation[schema.Verdict], error) {
			f.creates = append(f.creates, r)
			if f.createErr != nil {
				return nil, f.createErr
			}
			return &longrunning.Operation[schema.Verdict]{ID: r.ID}, nil
		},
		agent: func(ctx context.Context, r schema.AgentCreateRequest) (*schema.AgentCreateResponse, error) {
			f.agents = append(f.agents, r)
			return &schema.AgentCreateResponse{SessionID: "session-1"}, nil
		},
		cfg: cfg,
		now: testNow,
	}
}

func queued(pkg string, stage scheduler.Stage) scheduler.Campaign {
	return scheduler.Campaign{Ecosystem: "npm", Package: pkg, Version: "1.0.0", Artifact: pkg + "-1.0.0.tgz",
		Stage: stage, State: scheduler.StateQueued, Score: 0.5}
}

// inflight is a campaign dispatched age ago as run-0 (session-0 at the agent stage).
func inflight(pkg string, stage scheduler.Stage, age time.Duration) scheduler.Campaign {
	c := queued(pkg, stage)
	c.State, c.Attempts = scheduler.StateInFlight, 1
	c.LastRunID, c.LastSession, c.DispatchedAt = "run-0", "session-0", testNow.Add(-age)
	return c
}

func with(c scheduler.Campaign, edit func(*scheduler.Campaign)) scheduler.Campaign {
	edit(&c)
	return c
}

// claimed is c as launch leaves it: in flight under this pass's run id.
func claimed(c scheduler.Campaign, session string) scheduler.Campaign {
	return with(c, func(c *scheduler.Campaign) {
		c.State, c.Outcome = scheduler.StateInFlight, scheduler.OutcomePending
		c.LastRunID, c.LastSession = testRunID, session
		c.DispatchedAt, c.Updated = testNow, testNow
		c.Attempts++
	})
}

func TestPass(t *testing.T) {
	ctx := context.Background()
	cfg := dispatchConfig{Batch: 10, MaxAgent: 1, MaxRetries: 3, InflightTimeout: time.Hour}
	target := queued("p", scheduler.StageInfer).Target()
	for _, tc := range []struct {
		name     string
		campaign scheduler.Campaign
		attempt  *schema.RebuildAttempt
		session  *schema.AgentSession
		api      fakeAPI
		want     scheduler.Campaign
		wantSum  passSummary
	}{
		{
			name:     "QueuedInferIsClaimedThenCreated",
			campaign: queued("p", scheduler.StageInfer),
			want:     claimed(queued("p", scheduler.StageInfer), ""),
			wantSum:  passSummary{Dispatched: 1},
		},
		{
			// A failure escalates, and the escalated campaign is eligible in
			// the same pass.
			name:     "FailedAttemptEscalatesAndRedispatches",
			campaign: inflight("p", scheduler.StageInfer, time.Minute),
			attempt:  &schema.RebuildAttempt{RunID: "run-0", Status: schema.RebuildStatusFailure, Message: "rebuild content mismatch"},
			want: claimed(with(inflight("p", scheduler.StageInfer, time.Minute), func(c *scheduler.Campaign) {
				c.Stage = scheduler.StageAgent
			}), "session-1"),
			wantSum: passSummary{Observed: 1, Dispatched: 1, AgentDispatched: 1},
		},
		{
			name:     "PendingAttemptWaits",
			campaign: inflight("p", scheduler.StageInfer, time.Minute),
			attempt:  &schema.RebuildAttempt{RunID: "run-0", Status: schema.RebuildStatusRunning},
			want:     inflight("p", scheduler.StageInfer, time.Minute),
		},
		{
			// A dispatch that never reported back counts as transient once it
			// is past the timeout, and the retry is dispatched in the same pass.
			name:     "WedgedDispatchRetries",
			campaign: inflight("p", scheduler.StageInfer, 2*time.Hour),
			want: claimed(with(inflight("p", scheduler.StageInfer, 2*time.Hour), func(c *scheduler.Campaign) {
				c.Retries = 1
			}), ""),
			wantSum: passSummary{Observed: 1, Dispatched: 1},
		},
		{
			name:     "CompletedSessionFinishes",
			campaign: inflight("p", scheduler.StageAgent, time.Minute),
			session:  &schema.AgentSession{ID: "session-0", Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess},
			want: with(inflight("p", scheduler.StageAgent, time.Minute), func(c *scheduler.Campaign) {
				c.State, c.Outcome, c.Updated = scheduler.StateDone, scheduler.OutcomeAttested, testNow
			}),
			wantSum: passSummary{Observed: 1},
		},
		{
			// A launch the API refused must not leave a phantom dispatch to
			// time out.
			name:     "FailedLaunchReturnsClaim",
			campaign: queued("p", scheduler.StageInfer),
			api:      fakeAPI{createErr: errors.New("api down")},
			want:     queued("p", scheduler.StageInfer),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(cfg, &tc.api)
			if err := d.campaigns.Upsert(ctx, tc.campaign); err != nil {
				t.Fatal(err)
			}
			if tc.attempt != nil {
				a := *tc.attempt
				a.Ecosystem, a.Package, a.Version, a.Artifact = "npm", "p", "1.0.0", "p-1.0.0.tgz"
				if err := d.attempts.Upsert(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			if tc.session != nil {
				if err := d.sessions.Upsert(ctx, *tc.session); err != nil {
					t.Fatal(err)
				}
			}
			if diff := cmp.Diff(tc.wantSum, d.pass(ctx, []scheduler.Campaign{tc.campaign}, new(scheduler.Budget))); diff != "" {
				t.Errorf("pass summary mismatch (-want +got):\n%s", diff)
			}
			got, err := d.campaigns.Get(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("stored campaign mismatch (-want +got):\n%s", diff)
			}
			for _, r := range tc.api.creates {
				if r.ID != testRunID || r.ExecutionHint != schema.ExtendedExecution {
					t.Errorf("create request = %+v, want run id %q with extended execution", r, testRunID)
				}
			}
			for _, r := range tc.api.agents {
				if r.RunID != testRunID {
					t.Errorf("agent request run id = %q, want %q", r.RunID, testRunID)
				}
			}
		})
	}
	t.Run("AgentCapHolds", func(t *testing.T) {
		var f fakeAPI
		d := newTestDispatcher(cfg, &f)
		active := []scheduler.Campaign{queued("a", scheduler.StageAgent), queued("b", scheduler.StageAgent)}
		for _, c := range active {
			if err := d.campaigns.Upsert(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		if diff := cmp.Diff(passSummary{Dispatched: 1, AgentDispatched: 1}, d.pass(ctx, active, new(scheduler.Budget))); diff != "" {
			t.Errorf("pass summary mismatch (-want +got):\n%s", diff)
		}
		if len(f.agents) != 1 {
			t.Errorf("agent launches = %d, want 1", len(f.agents))
		}
	})
	t.Run("CompletionChargesBudget", func(t *testing.T) {
		var f fakeAPI
		d := newTestDispatcher(cfg, &f)
		c := inflight("p", scheduler.StageInfer, time.Minute)
		if err := d.campaigns.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
		a := schema.RebuildAttempt{Ecosystem: "npm", Package: "p", Version: "1.0.0", Artifact: "p-1.0.0.tgz", RunID: "run-0",
			Status: schema.RebuildStatusSuccess, Costs: &schema.AttemptCosts{BuilderSeconds: 90}}
		if err := d.attempts.Upsert(ctx, a); err != nil {
			t.Fatal(err)
		}
		var budget scheduler.Budget
		d.pass(ctx, []scheduler.Campaign{c}, &budget)
		ledger, err := d.budgets.Get(ctx, scheduler.BudgetScopeGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if budget.BuildSeconds != 90 || ledger.BuildSeconds != 90 {
			t.Errorf("build seconds local/ledger = %d/%d, want 90/90", budget.BuildSeconds, ledger.BuildSeconds)
		}
	})
}

// TestCaps covers how a pass bounds itself: the per-pass caps, the room left
// under the in-flight caps, and a spent period time cap admitting nothing.
func TestCaps(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cfg              dispatchConfig
		budget           scheduler.Budget
		builds, sessions int
		want             scheduler.Caps
	}{
		{name: "Unbounded"},
		{name: "PerPass", cfg: dispatchConfig{Batch: 10, MaxAgent: 2}, want: scheduler.Caps{Batch: 10, Agent: 2}},
		{name: "InflightRoom", cfg: dispatchConfig{MaxInflightBuilds: 5, MaxInflightSessions: 3}, builds: 2, sessions: 1, want: scheduler.Caps{Build: 3, Agent: 2}},
		{name: "InflightFull", cfg: dispatchConfig{MaxInflightBuilds: 5, MaxInflightSessions: 3}, builds: 5, sessions: 4, want: scheduler.Caps{Build: scheduler.CapNone, Agent: scheduler.CapNone}},
		{name: "TighterAgentCapWins", cfg: dispatchConfig{MaxAgent: 5, MaxInflightSessions: 3}, sessions: 1, want: scheduler.Caps{Agent: 2}},
		{name: "SpentTimeCap", cfg: dispatchConfig{Batch: 10, BuildTimeCap: time.Hour, SessionTimeCap: time.Hour}, budget: scheduler.Budget{BuildSeconds: 3600, SessionSeconds: 1800}, want: scheduler.Caps{Batch: 10, Build: scheduler.CapNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &dispatcher{cfg: tc.cfg}
			if diff := cmp.Diff(tc.want, d.caps(tc.budget, tc.builds, tc.sessions)); diff != "" {
				t.Errorf("caps mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCharge covers spend accounting: completions land on the global ledger and
// on the local copy that dispatch caps read, and an elapsed period resets the
// ledger before new spend lands on it.
func TestCharge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := &dispatcher{cfg: dispatchConfig{Period: 24 * time.Hour}, now: now, budgets: db.NewMemoryBudgets()}
	var local scheduler.Budget
	d.charge(ctx, &local, scheduler.Spend{BuildSeconds: 100})
	d.charge(ctx, &local, scheduler.Spend{SessionSeconds: 50, Tokens: 7})

	if local.BuildSeconds != 100 || local.SessionSeconds != 50 {
		t.Errorf("local copy = %+v, want 100 build / 50 session", local)
	}
	b, err := d.budgets.Get(ctx, scheduler.BudgetScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if b.Scope != scheduler.BudgetScopeGlobal || b.BuildSeconds != 100 || b.SessionSeconds != 50 || b.Tokens != 7 {
		t.Errorf("ledger = %+v, want the global scope with 100 build / 50 session / 7 tokens", b)
	}
	t.Run("ElapsedPeriodResetsBeforeCharging", func(t *testing.T) {
		d.now = now.Add(25 * time.Hour)
		d.charge(ctx, new(scheduler.Budget), scheduler.Spend{BuildSeconds: 5})
		b, err := d.budgets.Get(ctx, scheduler.BudgetScopeGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if b.BuildSeconds != 5 || !b.PeriodStart.Equal(d.now) {
			t.Errorf("rolled-over ledger = %+v, want only the fresh 5 build seconds", b)
		}
	})
}

// TestTransitionConflict covers the compare-and-swap that keeps overlapping
// passes from double-applying: a transition aborts when the stored state or
// run id no longer matches what this pass read.
func TestTransitionConflict(t *testing.T) {
	ctx := context.Background()
	d := newTestDispatcher(dispatchConfig{}, &fakeAPI{})
	c := with(queued("p", scheduler.StageInfer), func(c *scheduler.Campaign) { c.LastRunID = "run-1" })
	if err := d.campaigns.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	noop := func(*scheduler.Campaign) {}
	t.Run("StateMismatch", func(t *testing.T) {
		if _, err := d.transition(ctx, c, scheduler.StateInFlight, noop); !errors.Is(err, errConflict) {
			t.Errorf("transition = %v, want errConflict", err)
		}
	})
	t.Run("RunIDMismatch", func(t *testing.T) {
		stale := with(c, func(c *scheduler.Campaign) { c.LastRunID = "run-0" })
		if _, err := d.transition(ctx, stale, scheduler.StateQueued, noop); !errors.Is(err, errConflict) {
			t.Errorf("transition = %v, want errConflict", err)
		}
	})
	t.Run("CleanClaimThenReplayConflicts", func(t *testing.T) {
		got, err := d.transition(ctx, c, scheduler.StateQueued, func(cur *scheduler.Campaign) { cur.State = scheduler.StateInFlight })
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
		if got.State != scheduler.StateInFlight {
			t.Errorf("claimed campaign State = %v, want in-flight", got.State)
		}
		// A second pass working from the same snapshot must now lose.
		if _, err := d.transition(ctx, c, scheduler.StateQueued, noop); !errors.Is(err, errConflict) {
			t.Errorf("replayed transition = %v, want errConflict", err)
		}
	})
}
