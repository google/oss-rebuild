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
	"time"

	"cloud.google.com/go/firestore"
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
)

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

type dispatchConfig struct {
	Project           string
	API               string
	Batch             int
	MaxAgent          int
	AgentIterations   int
	InflightTimeout   time.Duration
	MaxRetries        int
	FreshnessK        float64
	FreshnessTauHours float64
}

func (c dispatchConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
	}
	return nil
}

// errConflict marks a transition that found the campaign no longer in the
// expected state: an overlapping pass got there first, and whatever it did
// stands.
var errConflict = errors.New("campaign changed underneath this pass")

// dispatcher holds one pass's dependencies. Every store has an in-memory
// implementation and both API stubs are function values, so a whole pass
// runs under test.
type dispatcher struct {
	io        cli.IO
	campaigns db.Campaigns
	attempts  db.Attempts
	sessions  db.Sessions
	create    api.StubFn[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]]
	agent     api.StubFn[schema.AgentCreateRequest, schema.AgentCreateResponse]
	cfg       dispatchConfig
	now       time.Time
}

type passSummary struct {
	Observed, Dispatched, AgentDispatched int
}

// pass observes every in-flight campaign, then dispatches the queued ones
// that fit under the caps. A campaign observed back into the queue is
// eligible again in the same pass.
func (d *dispatcher) pass(ctx context.Context, active []scheduler.Campaign) passSummary {
	var sum passSummary
	var queued []scheduler.Campaign
	for _, c := range active {
		if c.State == scheduler.StateInFlight {
			if next, ok := d.observe(ctx, c); ok {
				sum.Observed++
				c = next
			}
		}
		if c.State == scheduler.StateQueued {
			queued = append(queued, c)
		}
	}
	caps := scheduler.Caps{Batch: d.cfg.Batch, Agent: d.cfg.MaxAgent}
	for _, c := range scheduler.Select(queued, caps, d.now, d.cfg.FreshnessK, d.cfg.FreshnessTauHours) {
		if err := d.launch(ctx, c); err != nil {
			if !errors.Is(err, errConflict) {
				fmt.Fprintf(d.io.Err, "dispatch %s (%s): %v\n", scheduler.TargetID(c.Target()), c.Stage, err)
			}
			continue
		}
		sum.Dispatched++
		if c.Stage == scheduler.StageAgent {
			sum.AgentDispatched++
		}
	}
	return sum
}

// observe applies the outcome of an in-flight campaign's dispatch. A dispatch
// with no terminal result is left alone until it has been in flight past the
// timeout, when it counts as a transient outcome: a lost job is
// indistinguishable from a slow one, and Tick bounds how often that is
// tolerated.
func (d *dispatcher) observe(ctx context.Context, c scheduler.Campaign) (scheduler.Campaign, bool) {
	outcome, ok := d.outcome(ctx, c)
	if !ok {
		if d.cfg.InflightTimeout <= 0 || c.DispatchedAt.IsZero() || d.now.Sub(c.DispatchedAt) <= d.cfg.InflightTimeout {
			return c, false
		}
		outcome = scheduler.OutcomeTransient
	}
	next, err := d.transition(ctx, c, scheduler.StateInFlight, func(cur *scheduler.Campaign) {
		*cur = scheduler.Tick(*cur, outcome, d.cfg.MaxRetries, d.now)
	})
	return next, err == nil
}

// outcome reads the terminal result of the campaign's last dispatch. The
// second return is false while there is none.
func (d *dispatcher) outcome(ctx context.Context, c scheduler.Campaign) (scheduler.Outcome, bool) {
	if c.Stage == scheduler.StageAgent {
		s, err := d.sessions.Get(ctx, c.LastSession)
		if err != nil || s.Status != schema.AgentSessionStatusCompleted {
			return "", false
		}
		return scheduler.ClassifySession(s.StopReason), true
	}
	a, err := d.attempts.Get(ctx, db.AttemptKey{Target: c.Target(), RunID: c.LastRunID})
	if err != nil || !scheduler.IsTerminal(a.Status) {
		return "", false
	}
	return scheduler.ClassifyRebuild(a.Status, a.Message), true
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
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	client, apiURL, err := apiClient(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	d := &dispatcher{
		io:        deps.IO,
		campaigns: db.NewFirestoreCampaigns(fire),
		attempts:  db.NewFirestoreAttempts(fire),
		sessions:  db.NewFirestoreSessions(fire),
		create:    api.Stub[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]](client, apiURL.JoinPath("rebuild", "op", "create")),
		agent:     api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent")),
		cfg:       cfg,
		now:       time.Now().UTC(),
	}
	active, err := db.ListActiveCampaigns(ctx, fire)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	sum := d.pass(ctx, active)
	fmt.Fprintf(deps.IO.Out, "dispatch: observed %d, dispatched %d (%d agent)\n", sum.Observed, sum.Dispatched, sum.AgentDispatched)
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
	set.DurationVar(&cfg.InflightTimeout, "inflight-timeout", 24*time.Hour, "requeue dispatches in flight longer than this; 0 = never")
	set.IntVar(&cfg.MaxRetries, "max-retries", 5, "max same-stage transient retries before stopping for triage")
	set.Float64Var(&cfg.FreshnessK, "freshness-k", scheduler.DefaultFreshnessK, "freshness boost coefficient k in 1+k*exp(-age/tau)")
	set.Float64Var(&cfg.FreshnessTauHours, "freshness-tau-hours", scheduler.DefaultFreshnessTauHours, "freshness decay constant tau in hours")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
