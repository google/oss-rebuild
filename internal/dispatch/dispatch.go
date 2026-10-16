// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package dispatch runs onboarding dispatch passes, from ctl against a remote
// API or inside the API against its own handlers.
package dispatch

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
)

// Config bounds and shapes one pass. Zero caps are unbounded.
type Config struct {
	Batch               int
	MaxAgent            int
	AgentIterations     int
	AgentModel          string
	Workers             int
	BuildTimeCap        time.Duration
	SessionTimeCap      time.Duration
	Period              time.Duration
	MaxInflightBuilds   int
	MaxInflightSessions int
	InflightTimeout     time.Duration
	MaxRetries          int
	FreshnessK          float64
	FreshnessTauHours   float64
}

// Defaults is the configuration a pass runs with when nothing is specified.
var Defaults = Config{
	Batch:             50,
	MaxAgent:          5,
	AgentIterations:   5,
	Workers:           12,
	Period:            24 * time.Hour,
	InflightTimeout:   24 * time.Hour,
	MaxRetries:        5,
	FreshnessK:        scheduler.DefaultFreshnessK,
	FreshnessTauHours: scheduler.DefaultFreshnessTauHours,
}

// Validate implements act.Input.
func (c Config) Validate() error {
	if c.Workers < 1 {
		return errors.New("workers must be positive")
	}
	return nil
}

// Request is the form-encoded Config for the API's dispatch endpoint. Empty
// fields take Defaults and a negative value switches a cap off. Durations are
// Go duration strings ("24h").
type Request struct {
	Batch               int     `form:"batch"`
	MaxAgent            int     `form:"max_agent"`
	AgentIterations     int     `form:"agent_iterations"`
	AgentModel          string  `form:"agent_model"`
	Workers             int     `form:"workers"`
	BuildTimeCap        string  `form:"build_time_cap"`
	SessionTimeCap      string  `form:"session_time_cap"`
	Period              string  `form:"period"`
	MaxInflightBuilds   int     `form:"max_inflight_builds"`
	MaxInflightSessions int     `form:"max_inflight_sessions"`
	InflightTimeout     string  `form:"inflight_timeout"`
	MaxRetries          int     `form:"max_retries"`
	FreshnessK          float64 `form:"freshness_k"`
	FreshnessTauHours   float64 `form:"freshness_tau_hours"`
}

// Validate implements act.Input.
func (r Request) Validate() error {
	_, err := r.Config()
	return err
}

// Config resolves the request against Defaults.
func (r Request) Config() (Config, error) {
	c := Defaults
	num := func(dst *int, v int) {
		switch {
		case v < 0:
			*dst = 0 // explicit "unbounded"
		case v > 0:
			*dst = v
		}
	}
	num(&c.Batch, r.Batch)
	num(&c.MaxAgent, r.MaxAgent)
	num(&c.AgentIterations, r.AgentIterations)
	num(&c.Workers, r.Workers)
	num(&c.MaxInflightBuilds, r.MaxInflightBuilds)
	num(&c.MaxInflightSessions, r.MaxInflightSessions)
	num(&c.MaxRetries, r.MaxRetries)
	c.AgentModel = r.AgentModel
	if r.FreshnessK != 0 {
		c.FreshnessK = r.FreshnessK
	}
	if r.FreshnessTauHours != 0 {
		c.FreshnessTauHours = r.FreshnessTauHours
	}
	for _, f := range []struct {
		name string
		src  string
		dst  *time.Duration
	}{
		{"build_time_cap", r.BuildTimeCap, &c.BuildTimeCap},
		{"session_time_cap", r.SessionTimeCap, &c.SessionTimeCap},
		{"period", r.Period, &c.Period},
		{"inflight_timeout", r.InflightTimeout, &c.InflightTimeout},
	} {
		if f.src == "" {
			continue
		}
		d, err := time.ParseDuration(f.src)
		if err != nil {
			return c, errors.Wrapf(err, "parsing %s", f.name)
		}
		*f.dst = d
	}
	return c, c.Validate()
}

// Dispatcher holds one pass's dependencies. Per-campaign failures are
// reported to Err.
type Dispatcher struct {
	Err       io.Writer
	Campaigns db.Campaigns
	Budgets   db.Budgets
	Attempts  db.Attempts
	Sessions  db.Sessions
	Create    api.StubFn[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]]
	Agent     api.StubFn[schema.AgentCreateRequest, schema.AgentCreateResponse]
	Config    Config
	Now       time.Time

	mu sync.Mutex // guards state the pass's workers share
}

func (d *Dispatcher) errw() io.Writer {
	if d.Err == nil {
		return io.Discard
	}
	return d.Err
}

// Report is what one pass did and the period's spend after it.
type Report struct {
	Summary
	Spend scheduler.Budget
}

// String renders the one-line pass report.
func (r Report) String() string {
	return fmt.Sprintf("pass: queued infer=%d agent=%d | inflight builds=%d sessions=%d | observed %d | dispatched %d (%d agent; approved %d) | spend %s build, %s session, %d tokens",
		r.QueuedInfer, r.QueuedAgent, r.InflightBuilds, r.InflightSessions, r.Observed, r.Dispatched, r.AgentDispatched, r.Approved,
		time.Duration(r.Spend.BuildSeconds)*time.Second, time.Duration(r.Spend.SessionSeconds)*time.Second, r.Spend.Tokens)
}

// Once runs one pass over the campaigns list returns and reports it with the
// period's spend.
func (d *Dispatcher) Once(ctx context.Context, list func(context.Context) ([]scheduler.Campaign, error)) (*Report, error) {
	d.Now = time.Now().UTC()
	budget, err := d.rolledOverBudget(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "loading budget")
	}
	active, err := list(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	sum := d.Pass(ctx, active, &budget)
	return &Report{Summary: sum, Spend: budget}, nil
}

// errConflict marks a transition that found the campaign no longer in the
// expected state: an overlapping pass got there first, and whatever it did
// stands.
var errConflict = errors.New("campaign changed underneath this pass")

// Summary is what one pass saw and did.
type Summary struct {
	QueuedInfer, QueuedAgent              int // as of the start of the pass
	InflightBuilds, InflightSessions      int // including the pass's own dispatches
	Observed                              int
	Approved, Dispatched, AgentDispatched int
}

// Pass observes every in-flight campaign, then dispatches the queued ones
// that fit under the caps. A campaign observed back into the queue is
// eligible again in the same pass. The caps count what is still in flight
// after observation. Both phases fan out across Workers.
func (d *Dispatcher) Pass(ctx context.Context, active []scheduler.Campaign, budget *scheduler.Budget) Summary {
	var sum Summary
	for _, c := range active {
		if c.State != scheduler.StateQueued {
			continue
		}
		if c.Stage == scheduler.StageAgent {
			sum.QueuedAgent++
		} else {
			sum.QueuedInfer++
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(d.Config.Workers, 1))
	for i := range active {
		if active[i].State != scheduler.StateInFlight {
			continue
		}
		g.Go(func() error {
			if next, ok := d.observe(gctx, active[i], budget); ok {
				active[i] = next
				d.mu.Lock()
				sum.Observed++
				d.mu.Unlock()
			}
			return nil
		})
	}
	g.Wait()
	var queued []scheduler.Campaign
	var inflightBuilds, inflightSessions int
	for _, c := range active {
		switch {
		case c.State == scheduler.StateQueued:
			queued = append(queued, c)
		case c.State != scheduler.StateInFlight:
		case c.Stage == scheduler.StageAgent:
			inflightSessions++
		default:
			inflightBuilds++
		}
	}
	// Selection reserves slots up front, so a failed launch idles its slot
	// until the next pass retries it.
	caps := d.caps(*budget, inflightBuilds, inflightSessions)
	selected := scheduler.Select(queued, caps, d.Now, d.Config.FreshnessK, d.Config.FreshnessTauHours)
	sum.Approved = len(selected)
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(max(d.Config.Workers, 1))
	for _, c := range selected {
		g.Go(func() error {
			if err := d.launch(gctx, c); err != nil {
				if !errors.Is(err, errConflict) {
					fmt.Fprintf(d.errw(), "dispatch %s (%s): %v\n", scheduler.TargetID(c.Target()), c.Stage, err)
				}
				return nil
			}
			d.mu.Lock()
			sum.Dispatched++
			if c.Stage == scheduler.StageAgent {
				sum.AgentDispatched++
			}
			d.mu.Unlock()
			return nil
		})
	}
	g.Wait()
	sum.InflightBuilds = inflightBuilds + sum.Dispatched - sum.AgentDispatched
	sum.InflightSessions = inflightSessions + sum.AgentDispatched
	return sum
}

// caps bounds one pass: the per-pass batch and agent caps, the room left
// under the in-flight caps, and nothing for a stage whose period time cap is
// spent.
func (d *Dispatcher) caps(budget scheduler.Budget, inflightBuilds, inflightSessions int) scheduler.Caps {
	caps := scheduler.Caps{
		Batch: d.Config.Batch,
		Build: room(d.Config.MaxInflightBuilds, inflightBuilds),
		Agent: tighter(d.Config.MaxAgent, room(d.Config.MaxInflightSessions, inflightSessions)),
	}
	if spent(d.Config.BuildTimeCap, budget.BuildSeconds) {
		caps.Build = scheduler.CapNone
	}
	if spent(d.Config.SessionTimeCap, budget.SessionSeconds) {
		caps.Agent = scheduler.CapNone
	}
	return caps
}

// room is the cap left under limit with used already in flight: unbounded
// when limit is, CapNone once full.
func room(limit, used int) int {
	switch {
	case limit <= 0:
		return 0
	case used >= limit:
		return scheduler.CapNone
	default:
		return limit - used
	}
}

// tighter is the more restrictive of two caps.
func tighter(a, b int) int {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}

// spent reports whether a period's seconds have reached a time cap.
func spent(cap time.Duration, seconds int64) bool {
	return cap > 0 && time.Duration(seconds)*time.Second >= cap
}

// observe applies the outcome of an in-flight campaign's dispatch and charges
// its spend once the transition lands, so overlapping passes cannot both
// charge it. A dispatch with no result past the in-flight timeout counts as
// transient, since a lost job looks like a slow one.
func (d *Dispatcher) observe(ctx context.Context, c scheduler.Campaign, budget *scheduler.Budget) (scheduler.Campaign, bool) {
	outcome, spend, ok := d.outcome(ctx, c)
	if !ok {
		if d.Config.InflightTimeout <= 0 || c.DispatchedAt.IsZero() || d.Now.Sub(c.DispatchedAt) <= d.Config.InflightTimeout {
			return c, false
		}
		outcome = scheduler.OutcomeTransient
	}
	next, err := d.transition(ctx, c, scheduler.StateInFlight, func(cur *scheduler.Campaign) {
		*cur = scheduler.Tick(*cur, outcome, d.Config.MaxRetries, d.Now)
	})
	if err != nil {
		return c, false
	}
	d.charge(ctx, budget, spend)
	return next, true
}

// outcome reads the terminal result of the campaign's last dispatch and the
// spend it incurred. The last return is false while there is none.
func (d *Dispatcher) outcome(ctx context.Context, c scheduler.Campaign) (scheduler.Outcome, scheduler.Spend, bool) {
	if c.Stage == scheduler.StageAgent {
		s, err := d.Sessions.Get(ctx, c.LastSession)
		if err != nil || s.Status != schema.AgentSessionStatusCompleted {
			return "", scheduler.Spend{}, false
		}
		spend := scheduler.Spend{SessionSeconds: spanSeconds(s.Created, s.Updated), Tokens: sessionTokens(s.Usage)}
		return scheduler.ClassifySession(s.StopReason), spend, true
	}
	a, err := d.Attempts.Get(ctx, db.AttemptKey{Target: c.Target(), RunID: c.LastRunID})
	if err != nil || !scheduler.IsTerminal(a.Status) {
		return "", scheduler.Spend{}, false
	}
	return scheduler.ClassifyRebuild(a.Status, a.Message), scheduler.Spend{BuildSeconds: attemptBuildSeconds(a)}, true
}

// attemptBuildSeconds is the builder time GCB reported for the attempt. An
// attempt that never started a build has none and charges nothing, as does
// the rare build abandoned before GCB reported its times.
func attemptBuildSeconds(a schema.RebuildAttempt) int64 {
	if a.Costs == nil {
		return 0
	}
	return int64(a.Costs.BuilderSeconds)
}

// sessionTokens is the session's input plus output tokens, undiscounted for
// caching: a load proxy, not a cost.
func sessionTokens(u *schema.TokenUsage) int64 {
	if u == nil {
		return 0
	}
	return int64(u.Input + u.Output)
}

// spanSeconds is the whole seconds between two timestamps, 0 unless both are
// set and in order. Missing clocks must read as free rather than poisoning
// the caps.
func spanSeconds(start, end time.Time) int64 {
	if start.IsZero() || !end.After(start) {
		return 0
	}
	return int64(end.Sub(start).Seconds())
}

// rolledOverBudget reads the global ledger, resetting it first when its period
// has elapsed. Concurrent passes may both observe expiry, but the reset is
// idempotent: the loser's transaction retries against the winner's fresh
// ledger and finds nothing left to do.
func (d *Dispatcher) rolledOverBudget(ctx context.Context) (scheduler.Budget, error) {
	return d.mutateBudget(ctx, scheduler.BudgetScopeGlobal, func(b *scheduler.Budget) {
		if b.Scope == "" || b.Expired(d.Now, d.Config.Period) {
			*b = scheduler.Budget{Scope: scheduler.BudgetScopeGlobal, PeriodStart: d.Now, Updated: d.Now}
		}
	})
}

// mutateBudget applies fn to the scope's ledger, seeding it on first touch.
func (d *Dispatcher) mutateBudget(ctx context.Context, scope string, fn func(*scheduler.Budget)) (scheduler.Budget, error) {
	var out scheduler.Budget
	for {
		err := d.Budgets.Mutate(ctx, scope, func(b *scheduler.Budget) (bool, error) {
			fn(b)
			out = *b
			return true, nil
		})
		if !errors.Is(err, db.ErrNotFound) {
			return out, err
		}
		out = scheduler.Budget{}
		fn(&out)
		switch err := d.Budgets.Insert(ctx, out); {
		case err == nil:
			return out, nil
		case errors.Is(err, db.ErrAlreadyExists):
			continue // Lost the creation race: mutate the winner's document.
		default:
			return out, err
		}
	}
}

// charge records one completion's spend on the global ledger and on the local
// copy that dispatch caps read.
func (d *Dispatcher) charge(ctx context.Context, budget *scheduler.Budget, s scheduler.Spend) {
	d.mu.Lock()
	budget.Add(s)
	d.mu.Unlock()
	_, err := d.mutateBudget(ctx, scheduler.BudgetScopeGlobal, func(b *scheduler.Budget) {
		if b.Scope == "" || b.Expired(d.Now, d.Config.Period) {
			*b = scheduler.Budget{Scope: scheduler.BudgetScopeGlobal, PeriodStart: d.Now}
		}
		b.Add(s)
		b.Updated = d.Now
	})
	if err != nil {
		fmt.Fprintf(d.errw(), "charging budget: %v\n", err)
	}
}

// launch claims a queued campaign and starts its stage. The claim lands
// before the API call, so of two overlapping passes exactly one starts the
// work. A failed call restores the campaign as it was.
func (d *Dispatcher) launch(ctx context.Context, c scheduler.Campaign) error {
	runID := d.Now.Format(time.RFC3339Nano)
	claimed, err := d.transition(ctx, c, scheduler.StateQueued, func(cur *scheduler.Campaign) {
		cur.State = scheduler.StateInFlight
		cur.Outcome = scheduler.OutcomePending
		cur.LastRunID = runID
		cur.LastSession = ""
		cur.DispatchedAt = d.Now
		cur.Updated = d.Now
		cur.Attempts++
	})
	if err != nil {
		return err
	}
	session, err := d.start(ctx, claimed)
	if err != nil {
		if _, rerr := d.transition(ctx, claimed, scheduler.StateInFlight, func(cur *scheduler.Campaign) { *cur = c }); rerr != nil {
			fmt.Fprintf(d.errw(), "returning claim on %s: %v\n", scheduler.TargetID(c.Target()), rerr)
		}
		return err
	}
	if session != "" {
		if _, err := d.transition(ctx, claimed, scheduler.StateInFlight, func(cur *scheduler.Campaign) { cur.LastSession = session }); err != nil {
			fmt.Fprintf(d.errw(), "recording session for %s: %v\n", scheduler.TargetID(c.Target()), err)
		}
	}
	return nil
}

// start calls the API for the campaign's stage. Agent dispatches return the
// session id the outcome is later read from.
func (d *Dispatcher) start(ctx context.Context, c scheduler.Campaign) (string, error) {
	switch c.Stage {
	case scheduler.StageInfer:
		_, err := d.Create(ctx, schema.RebuildPackageRequest{
			Ecosystem:     rebuild.Ecosystem(c.Ecosystem),
			Package:       c.Package,
			Version:       c.Version,
			Artifact:      c.Artifact,
			ID:            c.LastRunID,
			ExecutionHint: schema.ExtendedExecution,
		})
		return "", err
	case scheduler.StageAgent:
		resp, err := d.Agent(ctx, schema.AgentCreateRequest{
			Target:        c.Target(),
			RunID:         c.LastRunID,
			MaxIterations: d.Config.AgentIterations,
			Model:         d.Config.AgentModel,
		})
		if err != nil {
			return "", err
		}
		return resp.SessionID, nil
	default:
		return "", errors.Errorf("undispatchable stage %q", c.Stage)
	}
}

// transition applies fn to the stored campaign, provided it is still in
// fromState with the run id c carries, and returns the stored result. The
// read-modify-write is atomic, so overlapping passes cannot both claim a
// campaign or both apply an outcome.
func (d *Dispatcher) transition(ctx context.Context, c scheduler.Campaign, fromState scheduler.State, fn func(*scheduler.Campaign)) (scheduler.Campaign, error) {
	var out scheduler.Campaign
	err := d.Campaigns.Mutate(ctx, c.Target(), func(cur *scheduler.Campaign) (bool, error) {
		if cur.State != fromState || cur.LastRunID != c.LastRunID {
			return false, errConflict
		}
		fn(cur)
		out = *cur
		return true, nil
	})
	return out, err
}
