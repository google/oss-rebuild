// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type dispatchConfig struct {
	Project             string
	API                 string
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

func (c dispatchConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
	}
	if c.Workers < 1 {
		return errors.New("workers must be positive")
	}
	return nil
}

// errConflict marks a transition that found the campaign no longer in the
// expected state: an overlapping pass got there first, and whatever it did
// stands.
var errConflict = errors.New("campaign changed underneath this pass")

// dispatcher holds one pass's dependencies.
type dispatcher struct {
	io        cli.IO
	campaigns db.Campaigns
	budgets   db.Budgets
	attempts  db.Attempts
	sessions  db.Sessions
	create    api.StubFn[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]]
	agent     api.StubFn[schema.AgentCreateRequest, schema.AgentCreateResponse]
	cfg       dispatchConfig
	now       time.Time
	mu        sync.Mutex // guards state the pass's workers share
}

// passSummary is what one pass saw and did.
type passSummary struct {
	QueuedInfer, QueuedAgent              int // as of the start of the pass
	InflightBuilds, InflightSessions      int // including the pass's own dispatches
	Observed                              int
	Approved, Dispatched, AgentDispatched int
}

// pass observes every in-flight campaign, then dispatches the queued ones
// that fit under the caps. A campaign observed back into the queue is
// eligible again in the same pass. The caps count what is still in flight
// after observation. Both phases fan out across Workers.
func (d *dispatcher) pass(ctx context.Context, active []scheduler.Campaign, budget *scheduler.Budget) passSummary {
	var sum passSummary
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
	g.SetLimit(d.cfg.Workers)
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
	selected := scheduler.Select(queued, caps, d.now, d.cfg.FreshnessK, d.cfg.FreshnessTauHours)
	sum.Approved = len(selected)
	g, gctx = errgroup.WithContext(ctx)
	g.SetLimit(d.cfg.Workers)
	for _, c := range selected {
		g.Go(func() error {
			if err := d.launch(gctx, c); err != nil {
				if !errors.Is(err, errConflict) {
					fmt.Fprintf(d.io.Err, "dispatch %s (%s): %v\n", scheduler.TargetID(c.Target()), c.Stage, err)
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
func (d *dispatcher) caps(budget scheduler.Budget, inflightBuilds, inflightSessions int) scheduler.Caps {
	caps := scheduler.Caps{
		Batch: d.cfg.Batch,
		Build: room(d.cfg.MaxInflightBuilds, inflightBuilds),
		Agent: tighter(d.cfg.MaxAgent, room(d.cfg.MaxInflightSessions, inflightSessions)),
	}
	if spent(d.cfg.BuildTimeCap, budget.BuildSeconds) {
		caps.Build = scheduler.CapNone
	}
	if spent(d.cfg.SessionTimeCap, budget.SessionSeconds) {
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
func (d *dispatcher) observe(ctx context.Context, c scheduler.Campaign, budget *scheduler.Budget) (scheduler.Campaign, bool) {
	outcome, spend, ok := d.outcome(ctx, c)
	if !ok {
		if d.cfg.InflightTimeout <= 0 || c.DispatchedAt.IsZero() || d.now.Sub(c.DispatchedAt) <= d.cfg.InflightTimeout {
			return c, false
		}
		outcome = scheduler.OutcomeTransient
	}
	next, err := d.transition(ctx, c, scheduler.StateInFlight, func(cur *scheduler.Campaign) {
		*cur = scheduler.Tick(*cur, outcome, d.cfg.MaxRetries, d.now)
	})
	if err != nil {
		return c, false
	}
	d.charge(ctx, budget, spend)
	return next, true
}

// outcome reads the terminal result of the campaign's last dispatch and the
// spend it incurred. The last return is false while there is none.
func (d *dispatcher) outcome(ctx context.Context, c scheduler.Campaign) (scheduler.Outcome, scheduler.Spend, bool) {
	if c.Stage == scheduler.StageAgent {
		s, err := d.sessions.Get(ctx, c.LastSession)
		if err != nil || s.Status != schema.AgentSessionStatusCompleted {
			return "", scheduler.Spend{}, false
		}
		spend := scheduler.Spend{SessionSeconds: spanSeconds(s.Created, s.Updated), Tokens: sessionTokens(s.Usage)}
		return scheduler.ClassifySession(s.StopReason), spend, true
	}
	a, err := d.attempts.Get(ctx, db.AttemptKey{Target: c.Target(), RunID: c.LastRunID})
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
func (d *dispatcher) rolledOverBudget(ctx context.Context) (scheduler.Budget, error) {
	return d.mutateBudget(ctx, scheduler.BudgetScopeGlobal, func(b *scheduler.Budget) {
		if b.Scope == "" || b.Expired(d.now, d.cfg.Period) {
			*b = scheduler.Budget{Scope: scheduler.BudgetScopeGlobal, PeriodStart: d.now, Updated: d.now}
		}
	})
}

// mutateBudget applies fn to the scope's ledger, seeding it on first touch.
func (d *dispatcher) mutateBudget(ctx context.Context, scope string, fn func(*scheduler.Budget)) (scheduler.Budget, error) {
	var out scheduler.Budget
	for {
		err := d.budgets.Mutate(ctx, scope, func(b *scheduler.Budget) (bool, error) {
			fn(b)
			out = *b
			return true, nil
		})
		if !errors.Is(err, db.ErrNotFound) {
			return out, err
		}
		out = scheduler.Budget{}
		fn(&out)
		switch err := d.budgets.Insert(ctx, out); {
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
func (d *dispatcher) charge(ctx context.Context, budget *scheduler.Budget, s scheduler.Spend) {
	d.mu.Lock()
	budget.Add(s)
	d.mu.Unlock()
	_, err := d.mutateBudget(ctx, scheduler.BudgetScopeGlobal, func(b *scheduler.Budget) {
		if b.Scope == "" || b.Expired(d.now, d.cfg.Period) {
			*b = scheduler.Budget{Scope: scheduler.BudgetScopeGlobal, PeriodStart: d.now}
		}
		b.Add(s)
		b.Updated = d.now
	})
	if err != nil {
		fmt.Fprintf(d.io.Err, "charging budget: %v\n", err)
	}
}

// launch claims a queued campaign and starts its stage. The claim lands
// before the API call, so of two overlapping passes exactly one starts the
// work. A failed call restores the campaign as it was.
func (d *dispatcher) launch(ctx context.Context, c scheduler.Campaign) error {
	runID := d.now.Format(time.RFC3339Nano)
	claimed, err := d.transition(ctx, c, scheduler.StateQueued, func(cur *scheduler.Campaign) {
		cur.State = scheduler.StateInFlight
		cur.Outcome = scheduler.OutcomePending
		cur.LastRunID = runID
		cur.LastSession = ""
		cur.DispatchedAt = d.now
		cur.Updated = d.now
		cur.Attempts++
	})
	if err != nil {
		return err
	}
	session, err := d.start(ctx, claimed)
	if err != nil {
		if _, rerr := d.transition(ctx, claimed, scheduler.StateInFlight, func(cur *scheduler.Campaign) { *cur = c }); rerr != nil {
			fmt.Fprintf(d.io.Err, "returning claim on %s: %v\n", scheduler.TargetID(c.Target()), rerr)
		}
		return err
	}
	if session != "" {
		if _, err := d.transition(ctx, claimed, scheduler.StateInFlight, func(cur *scheduler.Campaign) { cur.LastSession = session }); err != nil {
			fmt.Fprintf(d.io.Err, "recording session for %s: %v\n", scheduler.TargetID(c.Target()), err)
		}
	}
	return nil
}

// start calls the API for the campaign's stage. Agent dispatches return the
// session id the outcome is later read from.
func (d *dispatcher) start(ctx context.Context, c scheduler.Campaign) (string, error) {
	switch c.Stage {
	case scheduler.StageInfer:
		_, err := d.create(ctx, schema.RebuildPackageRequest{
			Ecosystem:     rebuild.Ecosystem(c.Ecosystem),
			Package:       c.Package,
			Version:       c.Version,
			Artifact:      c.Artifact,
			ID:            c.LastRunID,
			ExecutionHint: schema.ExtendedExecution,
		})
		return "", err
	case scheduler.StageAgent:
		resp, err := d.agent(ctx, schema.AgentCreateRequest{
			Target:        c.Target(),
			RunID:         c.LastRunID,
			MaxIterations: d.cfg.AgentIterations,
			Model:         d.cfg.AgentModel,
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
func (d *dispatcher) transition(ctx context.Context, c scheduler.Campaign, fromState scheduler.State, fn func(*scheduler.Campaign)) (scheduler.Campaign, error) {
	var out scheduler.Campaign
	err := d.campaigns.Mutate(ctx, c.Target(), func(cur *scheduler.Campaign) (bool, error) {
		if cur.State != fromState || cur.LastRunID != c.LastRunID {
			return false, errConflict
		}
		fn(cur)
		out = *cur
		return true, nil
	})
	return out, err
}

func dispatchHandler(ctx context.Context, cfg dispatchConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := deps.Firestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	client, apiURL, err := deps.APIClient(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	d := &dispatcher{
		io:        deps.IO,
		campaigns: db.NewFirestoreCampaigns(fire),
		budgets:   db.NewFirestoreBudgets(fire),
		attempts:  db.NewFirestoreAttempts(fire),
		sessions:  db.NewFirestoreSessions(fire),
		create:    api.Stub[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]](client, apiURL.JoinPath("rebuild", "op", "create")),
		agent:     api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent")),
		cfg:       cfg,
		now:       time.Now().UTC(),
	}
	budget, err := d.rolledOverBudget(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "loading budget")
	}
	active, err := db.ListActiveCampaigns(ctx, fire)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	sum := d.pass(ctx, active, &budget)
	fmt.Fprintf(deps.IO.Out, "pass: queued infer=%d agent=%d | inflight builds=%d sessions=%d | observed %d | dispatched %d (%d agent; approved %d) | spend %s build, %s session, %d tokens\n",
		sum.QueuedInfer, sum.QueuedAgent, sum.InflightBuilds, sum.InflightSessions, sum.Observed, sum.Dispatched, sum.AgentDispatched, sum.Approved,
		time.Duration(budget.BuildSeconds)*time.Second, time.Duration(budget.SessionSeconds)*time.Second, budget.Tokens)
	return &act.NoOutput{}, nil
}

// apiClient builds an HTTP client (Cloud Run-authorized when the endpoint is
// on run.app) and the parsed base URL.
func apiClient(ctx context.Context, endpoint string) (*http.Client, *url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parsing API endpoint")
	}
	if strings.Contains(u.Host, "run.app") {
		u.Scheme = "https"
		client, err := oauth.AuthorizedUserIDClient(ctx)
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating authorized HTTP client")
		}
		return client, u, nil
	}
	return http.DefaultClient, u, nil
}

func dispatchCommand() *cobra.Command {
	cfg := dispatchConfig{}
	cmd := &cobra.Command{
		Use:   "dispatch --project <project> --api <URI> [--batch N] [--max-agent N]",
		Short: "Run one dispatch pass: observe in-flight, dispatch queued, escalate",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[dispatchConfig], InitDeps, dispatchHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.IntVar(&cfg.Batch, "batch", 50, "max dispatches this pass; 0 = unbounded")
	set.IntVar(&cfg.MaxAgent, "max-agent", 5, "max agent-stage dispatches this pass; 0 = unbounded")
	set.IntVar(&cfg.AgentIterations, "agent-iterations", 5, "max agent iterations per session")
	set.StringVar(&cfg.AgentModel, "agent-model", "", "Gemini model id for agent sessions; empty uses the agent's default")
	set.IntVar(&cfg.Workers, "workers", 12, "concurrent observations and launches within a pass")
	set.DurationVar(&cfg.BuildTimeCap, "build-time-cap", 0, "build execution time cap per period; 0 = uncapped")
	set.DurationVar(&cfg.SessionTimeCap, "session-time-cap", 0, "agent session time cap per period; 0 = uncapped")
	set.DurationVar(&cfg.Period, "period", 24*time.Hour, "budget period length")
	set.IntVar(&cfg.MaxInflightBuilds, "max-inflight-builds", 0, "max concurrent builds in flight; 0 = uncapped")
	set.IntVar(&cfg.MaxInflightSessions, "max-inflight-sessions", 0, "max concurrent agent sessions in flight; 0 = uncapped")
	set.DurationVar(&cfg.InflightTimeout, "inflight-timeout", 24*time.Hour, "requeue dispatches in flight longer than this; 0 = never")
	set.IntVar(&cfg.MaxRetries, "max-retries", 5, "max same-stage transient retries before stopping for triage")
	set.Float64Var(&cfg.FreshnessK, "freshness-k", scheduler.DefaultFreshnessK, "freshness boost coefficient k in 1+k*exp(-age/tau)")
	set.Float64Var(&cfg.FreshnessTauHours, "freshness-tau-hours", scheduler.DefaultFreshnessTauHours, "freshness decay constant tau in hours")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
