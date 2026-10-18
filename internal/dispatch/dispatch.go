// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package dispatch runs onboarding dispatch passes, from ctl against a remote
// API or inside the API against its own handlers.
package dispatch

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	EvalModels          string // comma-separated, overrides AgentModel. Launches rotate through it, so repeat a model to weight it
	Workers             int
	BuildRate           int           // build launches per minute, or 0 for as fast as the workers allow
	Window              time.Duration // stop launching this long after the pass starts, or 0 to launch everything approved
	Refill              time.Duration // re-observe and refill this often until Window closes, or 0 for one pass
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
	BuildRate:         40, // under GCB's 60 build creates per minute, with room for the retries behind them
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
	EvalModels          string  `form:"eval_models"`
	Workers             int     `form:"workers"`
	BuildRate           int     `form:"build_rate"`
	Window              string  `form:"window"`
	Refill              string  `form:"refill"`
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
	num(&c.BuildRate, r.BuildRate)
	num(&c.MaxInflightBuilds, r.MaxInflightBuilds)
	num(&c.MaxInflightSessions, r.MaxInflightSessions)
	num(&c.MaxRetries, r.MaxRetries)
	c.AgentModel = r.AgentModel
	c.EvalModels = r.EvalModels
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
		{"window", r.Window, &c.Window},
		{"refill", r.Refill, &c.Refill},
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

	agents    int        // agent launches this run, counted across refills so EvalModels rotate evenly
	mu        sync.Mutex // guards state the pass's workers share
	agentHeld bool       // an agent launch failed for capacity this run, and every later one would repeat its zone sweep
	buildHeld bool       // an observed build was turned away for capacity this run. Rejected builds free their slots at once, so the caps alone would not slow launches.
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
	return fmt.Sprintf("pass: queued infer=%d agent=%d | inflight builds=%d sessions=%d | observed %d | dispatched %d (%d agent; approved %d, windowed %d, held %d, passes %d) | spend %s build, %s session, %d tokens",
		r.QueuedInfer, r.QueuedAgent, r.InflightBuilds, r.InflightSessions, r.Observed, r.Dispatched, r.AgentDispatched, r.Approved, r.Windowed, r.Held, r.Passes,
		time.Duration(r.Spend.BuildSeconds)*time.Second, time.Duration(r.Spend.SessionSeconds)*time.Second, r.Spend.Tokens)
}

// Once runs one pass over the campaigns list returns and reports it with the
// period's spend.
func (d *Dispatcher) Once(ctx context.Context, list func(context.Context) ([]scheduler.Campaign, error)) (*Report, error) {
	active, err := list(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	return d.pass(ctx, active)
}

// pass rolls the ledger over and runs one pass over active.
func (d *Dispatcher) pass(ctx context.Context, active []scheduler.Campaign) (*Report, error) {
	d.Now = time.Now().UTC()
	budget, err := d.rolledOverBudget(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "loading budget")
	}
	sum := d.Pass(ctx, active, &budget)
	sum.Passes = 1
	return &Report{Summary: sum, Spend: budget}, nil
}

// Run drives passes over list until Window closes. Each pass launches for at
// most Refill and the next re-observes, so a slot freed mid-window is
// refilled within a tick. A pass is not started with less of the window left
// than the last one took, so the run ends before Window rather than past it.
// A zero Refill or Window runs one pass. list is read once and inflight
// before each refill.
func (d *Dispatcher) Run(ctx context.Context, list, inflight func(context.Context) ([]scheduler.Campaign, error)) (*Report, error) {
	// --rolling reuses the Dispatcher, so clear the previous run's holds.
	d.agentHeld, d.buildHeld = false, false
	if d.Config.Refill <= 0 || d.Config.Window <= 0 {
		return d.Once(ctx, list)
	}
	window := d.Config.Window
	defer func() { d.Config.Window = window }()
	end := time.Now().Add(window)
	active, err := list(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	var total Report
	var took time.Duration
	for {
		start := time.Now()
		remaining := end.Sub(start)
		if remaining <= 0 || (total.Passes > 0 && remaining < took) {
			return &total, nil
		}
		if total.Passes > 0 {
			// Only this run's launches leave the queued set, and the
			// in-flight read shows them, so a refill skips re-reading the
			// queue.
			fresh, err := inflight(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "listing in-flight campaigns")
			}
			active = refresh(active, fresh)
		}
		d.Config.Window = min(d.Config.Refill, remaining)
		report, err := d.pass(ctx, active)
		if err != nil {
			return nil, err
		}
		took = time.Since(start)
		total = accumulate(total, *report)
		next := start.Add(d.Config.Refill)
		if !next.Before(end) {
			return &total, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Until(next)):
		}
	}
}

// refresh keeps active's queued entries that fresh does not show as launched
// and replaces its in-flight view with fresh.
func refresh(active, fresh []scheduler.Campaign) []scheduler.Campaign {
	launched := make(map[string]bool, len(fresh))
	for _, c := range fresh {
		launched[scheduler.TargetID(c.Target())] = true
	}
	out := make([]scheduler.Campaign, 0, len(active)+len(fresh))
	for _, c := range active {
		if c.State == scheduler.StateQueued && !launched[scheduler.TargetID(c.Target())] {
			out = append(out, c)
		}
	}
	return append(out, fresh...)
}

// accumulate folds one pass's report into the run's. Activity counts add up,
// and the queue, in-flight, and spend figures are the latest pass's.
func accumulate(total, pass Report) Report {
	pass.Observed += total.Observed
	pass.Approved += total.Approved
	pass.Dispatched += total.Dispatched
	pass.AgentDispatched += total.AgentDispatched
	pass.Held += total.Held
	pass.Passes += total.Passes
	return pass
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
	Windowed                              int // approved launches left for the next pass when the window closed
	Held                                  int // launches skipped after one was turned away for capacity
	Passes                                int // passes behind these counts
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
	// A window keeps scheduled passes from overlapping. Launches it cuts off
	// stay queued for the next pass.
	var pace <-chan time.Time
	if d.Config.BuildRate > 0 {
		t := time.NewTicker(time.Minute / time.Duration(d.Config.BuildRate))
		defer t.Stop()
		pace = t.C
	}
	var deadline time.Time
	if d.Config.Window > 0 {
		deadline = time.Now().Add(d.Config.Window)
	}
	// Agent launches get their own workers so a scratch VM allocation, which
	// sweeps every zone before failing, does not hold up builds.
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(max(d.Config.Workers, 1))
	ga, gactx := errgroup.WithContext(ctx)
	ga.SetLimit(max(d.Config.Workers, 1))
launch:
	for i, c := range selected {
		if !deadline.IsZero() && time.Now().After(deadline) {
			sum.Windowed = len(selected) - i
			break
		}
		d.mu.Lock()
		held := d.buildHeld
		if c.Stage == scheduler.StageAgent {
			held = d.agentHeld
		}
		if held {
			sum.Held++
		}
		d.mu.Unlock()
		if held {
			continue
		}
		pool, lctx := g, gctx
		if c.Stage == scheduler.StageAgent {
			pool, lctx = ga, gactx
		} else if pace != nil {
			select {
			case <-pace:
			case <-ctx.Done():
				sum.Windowed = len(selected) - i
				break launch
			}
		}
		pool.Go(func() error {
			var model string
			if c.Stage == scheduler.StageAgent {
				// Scheduled while an earlier launch was still sweeping zones.
				d.mu.Lock()
				held := d.agentHeld
				if held {
					sum.Held++
				} else {
					// Only a launch that goes ahead takes a turn in the rotation.
					model = d.agentModel(d.agents)
					d.agents++
				}
				d.mu.Unlock()
				if held {
					return nil
				}
			}
			if err := d.launch(lctx, c, model); err != nil {
				if !errors.Is(err, errConflict) {
					fmt.Fprintf(d.errw(), "dispatch %s (%s): %v\n", scheduler.TargetID(c.Target()), c.Stage, err)
				}
				if c.Stage == scheduler.StageAgent && status.Code(err) == codes.ResourceExhausted {
					d.mu.Lock()
					d.agentHeld = true
					d.mu.Unlock()
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
	ga.Wait()
	g.Wait()
	sum.InflightBuilds = inflightBuilds + sum.Dispatched - sum.AgentDispatched
	sum.InflightSessions = inflightSessions + sum.AgentDispatched
	return sum
}

// agentModel is the model for a run's nth agent launch. Eval models
// round-robin so neither A/B arm starves the other out of the shared pool.
func (d *Dispatcher) agentModel(n int) string {
	models := splitNonEmpty(d.Config.EvalModels)
	if len(models) == 0 {
		return d.Config.AgentModel
	}
	return models[n%len(models)]
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
	outcome, spend, capacity, ok := d.outcome(ctx, c)
	if capacity && c.Stage != scheduler.StageAgent {
		d.mu.Lock()
		d.buildHeld = true
		d.mu.Unlock()
	}
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
	d.charge(ctx, budget, next, spend)
	return next, true
}

// outcome reads the terminal result of the campaign's last dispatch and the
// spend it incurred. capacity is set when a build attempt was turned away by
// a service with no room. The last return is false while there is no result.
func (d *Dispatcher) outcome(ctx context.Context, c scheduler.Campaign) (outcome scheduler.Outcome, spend scheduler.Spend, capacity, ok bool) {
	if c.Stage == scheduler.StageAgent {
		s, err := d.Sessions.Get(ctx, c.LastSession)
		if err != nil || s.Status != schema.AgentSessionStatusCompleted {
			return "", scheduler.Spend{}, false, false
		}
		spend := scheduler.Spend{SessionSeconds: spanSeconds(s.Created, s.Updated), Tokens: sessionTokens(s.Usage)}
		if scheduler.IsOversize(s.Summary) {
			return scheduler.OutcomeOversize, spend, false, true
		}
		return scheduler.ClassifySession(s.StopReason), spend, false, true
	}
	a, err := d.Attempts.Get(ctx, db.AttemptKey{Target: c.Target(), RunID: c.LastRunID})
	if err != nil || !scheduler.IsTerminal(a.Status) {
		return "", scheduler.Spend{}, false, false
	}
	return scheduler.ClassifyRebuild(a.Status, a.Message), scheduler.Spend{BuildSeconds: attemptBuildSeconds(a)}, scheduler.IsCapacityRejection(a.Message), true
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

// charge records one completion's spend on the global ledger, the campaign's
// ecosystem ledger, and the local copy that dispatch caps read. Both ledgers
// take the local copy's period, so an ecosystem's spend covers the same window
// as the global total.
func (d *Dispatcher) charge(ctx context.Context, budget *scheduler.Budget, c scheduler.Campaign, s scheduler.Spend) {
	d.mu.Lock()
	budget.Add(s)
	start := budget.PeriodStart
	d.mu.Unlock()
	for _, scope := range []string{scheduler.BudgetScopeGlobal, scheduler.BudgetScopeEcosystem(c.Ecosystem)} {
		_, err := d.mutateBudget(ctx, scope, func(b *scheduler.Budget) {
			if b.Scope == "" || b.PeriodStart.Before(start) {
				*b = scheduler.Budget{Scope: scope, PeriodStart: start}
			}
			b.Add(s)
			b.Updated = d.Now
		})
		if err != nil {
			fmt.Fprintf(d.errw(), "charging %s ledger: %v\n", scope, err)
		}
	}
}

// launch claims a queued campaign and starts its stage. The claim lands
// before the API call, so of two overlapping passes exactly one starts the
// work. A failed call restores the campaign as it was.
func (d *Dispatcher) launch(ctx context.Context, c scheduler.Campaign, model string) error {
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
	session, err := d.start(ctx, claimed, model)
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

// inferBuildTimeout keeps a hung build from holding a slot for the 23h
// default. An hour is empirical: infer builds take minutes, and the slowest
// successful ones still finish within it.
const inferBuildTimeout = time.Hour

// start calls the API for the campaign's stage. Agent dispatches run under
// model and return the session id the outcome is later read from.
func (d *Dispatcher) start(ctx context.Context, c scheduler.Campaign, model string) (string, error) {
	switch c.Stage {
	case scheduler.StageInfer:
		_, err := d.Create(ctx, schema.RebuildPackageRequest{
			Ecosystem:     rebuild.Ecosystem(c.Ecosystem),
			Package:       c.Package,
			Version:       c.Version,
			Artifact:      c.Artifact,
			ID:            c.LastRunID,
			ExecutionHint: schema.ExtendedExecution,
			BuildTimeout:  inferBuildTimeout,
		})
		return "", err
	case scheduler.StageAgent:
		resp, err := d.Agent(ctx, schema.AgentCreateRequest{
			Target:        c.Target(),
			RunID:         c.LastRunID,
			MaxIterations: d.Config.AgentIterations,
			Model:         model,
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

func splitNonEmpty(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
