// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	agentErr  error
}

func newTestDispatcher(cfg Config, f *fakeAPI) *Dispatcher {
	var mu sync.Mutex // launches run on workers
	return &Dispatcher{
		Campaigns: db.NewMemoryCampaigns(),
		Budgets:   db.NewMemoryBudgets(),
		Attempts:  db.NewMemoryAttempts(),
		Sessions:  db.NewMemorySessions(),
		Create: func(ctx context.Context, r schema.RebuildPackageRequest) (*longrunning.Operation[schema.Verdict], error) {
			mu.Lock()
			defer mu.Unlock()
			f.creates = append(f.creates, r)
			if f.createErr != nil {
				return nil, f.createErr
			}
			return &longrunning.Operation[schema.Verdict]{ID: r.ID}, nil
		},
		Agent: func(ctx context.Context, r schema.AgentCreateRequest) (*schema.AgentCreateResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			f.agents = append(f.agents, r)
			if f.agentErr != nil {
				return nil, f.agentErr
			}
			return &schema.AgentCreateResponse{SessionID: "session-1"}, nil
		},
		Config: cfg,
		Now:    testNow,
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
	cfg := Config{Batch: 10, MaxAgent: 1, MaxRetries: 3, InflightTimeout: time.Hour, Workers: 4}
	target := queued("p", scheduler.StageInfer).Target()
	for _, tc := range []struct {
		name     string
		campaign scheduler.Campaign
		attempt  *schema.RebuildAttempt
		session  *schema.AgentSession
		api      fakeAPI
		want     scheduler.Campaign
		wantSum  Summary
	}{
		{
			name:     "QueuedInferIsClaimedThenCreated",
			campaign: queued("p", scheduler.StageInfer),
			want:     claimed(queued("p", scheduler.StageInfer), ""),
			wantSum:  Summary{QueuedInfer: 1, InflightBuilds: 1, Approved: 1, Dispatched: 1},
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
			wantSum: Summary{InflightSessions: 1, Observed: 1, Approved: 1, Dispatched: 1, AgentDispatched: 1},
		},
		{
			name:     "PendingAttemptWaits",
			campaign: inflight("p", scheduler.StageInfer, time.Minute),
			attempt:  &schema.RebuildAttempt{RunID: "run-0", Status: schema.RebuildStatusRunning},
			want:     inflight("p", scheduler.StageInfer, time.Minute),
			wantSum:  Summary{InflightBuilds: 1},
		},
		{
			// A dispatch that never reported back counts as transient once it
			// is past the timeout, and the retry is dispatched in the same pass.
			name:     "WedgedDispatchRetries",
			campaign: inflight("p", scheduler.StageInfer, 2*time.Hour),
			want: claimed(with(inflight("p", scheduler.StageInfer, 2*time.Hour), func(c *scheduler.Campaign) {
				c.Retries = 1
			}), ""),
			wantSum: Summary{InflightBuilds: 1, Observed: 1, Approved: 1, Dispatched: 1},
		},
		{
			name:     "CompletedSessionFinishes",
			campaign: inflight("p", scheduler.StageAgent, time.Minute),
			session:  &schema.AgentSession{ID: "session-0", Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess},
			want: with(inflight("p", scheduler.StageAgent, time.Minute), func(c *scheduler.Campaign) {
				c.State, c.Outcome, c.Updated = scheduler.StateDone, scheduler.OutcomeAttested, testNow
			}),
			wantSum: Summary{Observed: 1},
		},
		{
			// A launch the API refused must not leave a phantom dispatch to
			// time out.
			name:     "FailedLaunchReturnsClaim",
			campaign: queued("p", scheduler.StageInfer),
			api:      fakeAPI{createErr: errors.New("api down")},
			want:     queued("p", scheduler.StageInfer),
			wantSum:  Summary{QueuedInfer: 1, Approved: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(cfg, &tc.api)
			if err := d.Campaigns.Upsert(ctx, tc.campaign); err != nil {
				t.Fatal(err)
			}
			if tc.attempt != nil {
				a := *tc.attempt
				a.Ecosystem, a.Package, a.Version, a.Artifact = "npm", "p", "1.0.0", "p-1.0.0.tgz"
				if err := d.Attempts.Upsert(ctx, a); err != nil {
					t.Fatal(err)
				}
			}
			if tc.session != nil {
				if err := d.Sessions.Upsert(ctx, *tc.session); err != nil {
					t.Fatal(err)
				}
			}
			if diff := cmp.Diff(tc.wantSum, d.Pass(ctx, []scheduler.Campaign{tc.campaign}, new(scheduler.Budget))); diff != "" {
				t.Errorf("pass summary mismatch (-want +got):\n%s", diff)
			}
			got, err := d.Campaigns.Get(ctx, target)
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
			if err := d.Campaigns.Upsert(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		if diff := cmp.Diff(Summary{QueuedAgent: 2, InflightSessions: 1, Approved: 1, Dispatched: 1, AgentDispatched: 1}, d.Pass(ctx, active, new(scheduler.Budget))); diff != "" {
			t.Errorf("pass summary mismatch (-want +got):\n%s", diff)
		}
		if len(f.agents) != 1 {
			t.Errorf("agent launches = %d, want 1", len(f.agents))
		}
	})
	t.Run("PacedBuildsSpaceOut", func(t *testing.T) {
		var f fakeAPI
		d := newTestDispatcher(func() Config { c := cfg; c.BuildRate = 1200; return c }(), &f) // one launch per 50ms
		active := []scheduler.Campaign{queued("a", scheduler.StageInfer), queued("b", scheduler.StageInfer), queued("c", scheduler.StageInfer)}
		for _, c := range active {
			if err := d.Campaigns.Upsert(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		sum := d.Pass(ctx, active, new(scheduler.Budget))
		if sum.Dispatched != 3 || sum.Windowed != 0 {
			t.Errorf("summary = %+v, want 3 dispatched, none windowed", sum)
		}
		if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
			t.Errorf("3 launches at 1200/min took %s, want at least 150ms", elapsed)
		}
	})
	t.Run("WindowLeavesTheRestQueued", func(t *testing.T) {
		var f fakeAPI
		d := newTestDispatcher(func() Config { c := cfg; c.Window = time.Nanosecond; return c }(), &f)
		active := []scheduler.Campaign{queued("a", scheduler.StageInfer), queued("b", scheduler.StageInfer)}
		for _, c := range active {
			if err := d.Campaigns.Upsert(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		sum := d.Pass(ctx, active, new(scheduler.Budget))
		if sum.Dispatched != 0 || sum.Windowed != 2 || len(f.creates) != 0 {
			t.Errorf("summary = %+v with %d creates, want nothing launched and 2 windowed", sum, len(f.creates))
		}
	})
	t.Run("CompletionChargesBudget", func(t *testing.T) {
		var f fakeAPI
		d := newTestDispatcher(cfg, &f)
		c := inflight("p", scheduler.StageInfer, time.Minute)
		if err := d.Campaigns.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
		a := schema.RebuildAttempt{Ecosystem: "npm", Package: "p", Version: "1.0.0", Artifact: "p-1.0.0.tgz", RunID: "run-0",
			Status: schema.RebuildStatusSuccess, Costs: &schema.AttemptCosts{BuilderSeconds: 90}}
		if err := d.Attempts.Upsert(ctx, a); err != nil {
			t.Fatal(err)
		}
		var budget scheduler.Budget
		d.Pass(ctx, []scheduler.Campaign{c}, &budget)
		ledger, err := d.Budgets.Get(ctx, scheduler.BudgetScopeGlobal)
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
		cfg              Config
		budget           scheduler.Budget
		builds, sessions int
		want             scheduler.Caps
	}{
		{name: "Unbounded"},
		{name: "PerPass", cfg: Config{Batch: 10, MaxAgent: 2}, want: scheduler.Caps{Batch: 10, Agent: 2}},
		{name: "InflightRoom", cfg: Config{MaxInflightBuilds: 5, MaxInflightSessions: 3}, builds: 2, sessions: 1, want: scheduler.Caps{Build: 3, Agent: 2}},
		{name: "InflightFull", cfg: Config{MaxInflightBuilds: 5, MaxInflightSessions: 3}, builds: 5, sessions: 4, want: scheduler.Caps{Build: scheduler.CapNone, Agent: scheduler.CapNone}},
		{name: "TighterAgentCapWins", cfg: Config{MaxAgent: 5, MaxInflightSessions: 3}, sessions: 1, want: scheduler.Caps{Agent: 2}},
		{name: "SpentTimeCap", cfg: Config{Batch: 10, BuildTimeCap: time.Hour, SessionTimeCap: time.Hour}, budget: scheduler.Budget{BuildSeconds: 3600, SessionSeconds: 1800}, want: scheduler.Caps{Batch: 10, Build: scheduler.CapNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{Config: tc.cfg}
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
	d := &Dispatcher{Config: Config{Period: 24 * time.Hour}, Now: now, Budgets: db.NewMemoryBudgets()}
	var local scheduler.Budget
	d.charge(ctx, &local, scheduler.Spend{BuildSeconds: 100})
	d.charge(ctx, &local, scheduler.Spend{SessionSeconds: 50, Tokens: 7})

	if local.BuildSeconds != 100 || local.SessionSeconds != 50 {
		t.Errorf("local copy = %+v, want 100 build / 50 session", local)
	}
	b, err := d.Budgets.Get(ctx, scheduler.BudgetScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if b.Scope != scheduler.BudgetScopeGlobal || b.BuildSeconds != 100 || b.SessionSeconds != 50 || b.Tokens != 7 {
		t.Errorf("ledger = %+v, want the global scope with 100 build / 50 session / 7 tokens", b)
	}
	t.Run("ElapsedPeriodResetsBeforeCharging", func(t *testing.T) {
		d.Now = now.Add(25 * time.Hour)
		d.charge(ctx, new(scheduler.Budget), scheduler.Spend{BuildSeconds: 5})
		b, err := d.Budgets.Get(ctx, scheduler.BudgetScopeGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if b.BuildSeconds != 5 || !b.PeriodStart.Equal(d.Now) {
			t.Errorf("rolled-over ledger = %+v, want only the fresh 5 build seconds", b)
		}
	})
}

// TestTransitionConflict covers the compare-and-swap that keeps overlapping
// passes from double-applying: a transition aborts when the stored state or
// run id no longer matches what this pass read.
func TestTransitionConflict(t *testing.T) {
	ctx := context.Background()
	d := newTestDispatcher(Config{}, &fakeAPI{})
	c := with(queued("p", scheduler.StageInfer), func(c *scheduler.Campaign) { c.LastRunID = "run-1" })
	if err := d.Campaigns.Upsert(ctx, c); err != nil {
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

// TestRequestConfig covers the HTTP form of the config: empty fields take
// Defaults, negatives switch a cap off, durations are Go strings.
func TestRequestConfig(t *testing.T) {
	got, err := Request{MaxAgent: -1, Batch: 200, Period: "1h", Window: "4m", AgentModel: "gemini-3.7-flash"}.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	want := Defaults
	want.MaxAgent, want.Batch, want.Period, want.Window, want.AgentModel = 0, 200, time.Hour, 4*time.Minute, "gemini-3.7-flash"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
	for name, r := range map[string]Request{
		"bad duration": {Period: "soon"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

func TestRunRefillsUntilTheWindowCloses(t *testing.T) {
	ctx := context.Background()
	seed := []scheduler.Campaign{queued("a", scheduler.StageInfer), queued("b", scheduler.StageInfer), queued("c", scheduler.StageInfer)}
	for _, tc := range []struct {
		name         string
		cfg          Config
		wantPasses   int // at least
		wantDispatch int
	}{
		{"OnePassWithoutRefill", Config{Batch: 1, Workers: 1, Window: 200 * time.Millisecond}, 1, 1},
		{"RefillRunsPassesUntilTheWindowCloses", Config{Batch: 1, Workers: 1, Window: 300 * time.Millisecond, Refill: 20 * time.Millisecond}, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(tc.cfg, &fakeAPI{})
			for _, c := range seed {
				if err := d.Campaigns.Upsert(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			// A live listing, as the API's Firestore one is, so each pass
			// sees what the previous ones dispatched.
			list := func(ctx context.Context) ([]scheduler.Campaign, error) {
				var out []scheduler.Campaign
				for _, c := range seed {
					got, err := d.Campaigns.Get(ctx, c.Target())
					if err != nil {
						return nil, err
					}
					out = append(out, got)
				}
				return out, nil
			}
			inflight := func(ctx context.Context) ([]scheduler.Campaign, error) {
				all, err := list(ctx)
				if err != nil {
					return nil, err
				}
				var out []scheduler.Campaign
				for _, c := range all {
					if c.State == scheduler.StateInFlight {
						out = append(out, c)
					}
				}
				return out, nil
			}
			report, err := d.Run(ctx, list, inflight)
			if err != nil {
				t.Fatal(err)
			}
			if report.Passes < tc.wantPasses || report.Dispatched != tc.wantDispatch {
				t.Errorf("passes %d dispatched %d, want at least %d passes and %d dispatched", report.Passes, report.Dispatched, tc.wantPasses, tc.wantDispatch)
			}
			if d.Config.Window != tc.cfg.Window {
				t.Errorf("window left at %s, want %s restored", d.Config.Window, tc.cfg.Window)
			}
		})
	}
}

// TestPassHoldsAgentLaunchesAfterCapacityFailure: once a session cannot get
// a VM, the run stops trying, and builds still launch.
func TestPassHoldsAgentLaunchesAfterCapacityFailure(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPI{agentErr: status.Error(codes.ResourceExhausted, "allocating scratch: insert instance: all zones exhausted")}
	// One launch at a time, so the failure lands before the next is scheduled.
	d := newTestDispatcher(Config{Batch: 10, MaxAgent: 5, MaxRetries: 3, InflightTimeout: time.Hour, Workers: 1}, f)
	var active []scheduler.Campaign
	for _, name := range []string{"a1", "a2", "a3"} {
		c := queued(name, scheduler.StageAgent)
		d.Campaigns.Upsert(ctx, c)
		active = append(active, c)
	}
	b := queued("b", scheduler.StageInfer)
	d.Campaigns.Upsert(ctx, b)
	active = append(active, b)
	budget := scheduler.Budget{}
	sum := d.Pass(ctx, active, &budget)
	if len(f.agents) != 1 || sum.Held != 2 || sum.AgentDispatched != 0 {
		t.Errorf("agent launches attempted %d, held %d, dispatched %d; want 1, 2, 0", len(f.agents), sum.Held, sum.AgentDispatched)
	}
	if len(f.creates) != 1 || sum.Dispatched != 1 {
		t.Errorf("builds launched %d (dispatched %d); want 1", len(f.creates), sum.Dispatched)
	}
	before := len(f.agents)
	sum = d.Pass(ctx, active, &budget)
	if len(f.agents) != before || sum.Held != 3 {
		t.Errorf("a later pass in the run attempted %d agent launches, held %d; want 0 and 3", len(f.agents)-before, sum.Held)
	}
	d.agentHeld = false // a new run
	sum = d.Pass(ctx, active, &budget)
	if len(f.agents) != before+1 {
		t.Errorf("a new run attempted %d agent launches; want 1", len(f.agents)-before)
	}
}

func TestPassHoldsBuildLaunchesAfterCapacityRejection(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPI{}
	d := newTestDispatcher(Config{Batch: 10, MaxRetries: 3, InflightTimeout: time.Hour, Workers: 1}, f)
	// A build whose inference was turned away by Cloud Run comes back this pass.
	rejected := inflight("r", scheduler.StageInfer, time.Minute)
	d.Campaigns.Upsert(ctx, rejected)
	d.Attempts.Upsert(ctx, schema.RebuildAttempt{Ecosystem: rejected.Ecosystem, Package: rejected.Package, Version: rejected.Version, Artifact: rejected.Artifact,
		RunID: "run-0", Status: schema.RebuildStatusError,
		Message: "getting strategy: fetching inference version: rpc error: code = ResourceExhausted desc = resource exhausted"})
	active := []scheduler.Campaign{rejected}
	for _, name := range []string{"b1", "b2"} {
		c := queued(name, scheduler.StageInfer)
		d.Campaigns.Upsert(ctx, c)
		active = append(active, c)
	}
	budget := scheduler.Budget{}
	sum := d.Pass(ctx, active, &budget)
	got, _ := d.Campaigns.Get(ctx, rejected.Target())
	if got.Stage != scheduler.StageInfer || got.State != scheduler.StateQueued || got.Retries != 1 {
		t.Errorf("rejected campaign = stage %s state %s retries %d; want infer QUEUED 1", got.Stage, got.State, got.Retries)
	}
	if len(f.creates) != 0 || sum.Held != 3 || sum.Dispatched != 0 {
		t.Errorf("builds launched %d, held %d, dispatched %d; want 0, 3, 0", len(f.creates), sum.Held, sum.Dispatched)
	}
	d.buildHeld = false // a new run
	sum = d.Pass(ctx, active, &budget)
	if len(f.creates) != 3 || sum.Held != 0 {
		t.Errorf("a new run launched %d builds, held %d; want 3, 0", len(f.creates), sum.Held)
	}
}
