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

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/dispatch"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// dispatchConfig drives passes from this machine against the API. The
// deployment also runs them inside the API on onboard_dispatch_schedule.
type dispatchConfig struct {
	Project string
	API     string
	Rolling time.Duration
	Pass    dispatch.Config
}

func (c dispatchConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
	}
	return c.Pass.Validate()
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
	d := &dispatch.Dispatcher{
		Err:       deps.IO.Err,
		Campaigns: db.NewFirestoreCampaigns(fire),
		Budgets:   db.NewFirestoreBudgets(fire),
		Attempts:  db.NewFirestoreAttempts(fire),
		Sessions:  db.NewFirestoreSessions(fire),
		Create:    api.Stub[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]](client, apiURL.JoinPath("rebuild", "op", "create")),
		Agent:     api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent")),
		Config:    cfg.Pass,
	}
	list := func(ctx context.Context) ([]scheduler.Campaign, error) { return db.ListActiveCampaigns(ctx, fire) }
	inflight := func(ctx context.Context) ([]scheduler.Campaign, error) { return db.ListInFlightCampaigns(ctx, fire) }
	for {
		if report, err := d.Run(ctx, list, inflight); err != nil {
			if cfg.Rolling == 0 {
				return nil, err
			}
			// A failed pass under --rolling is reported, not fatal.
			fmt.Fprintf(deps.IO.Err, "pass failed: %v\n", err)
		} else {
			fmt.Fprintln(deps.IO.Out, report)
			if cfg.Rolling == 0 {
				return &act.NoOutput{}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cfg.Rolling):
		}
	}
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
	cfg := dispatchConfig{Pass: dispatch.Defaults}
	cmd := &cobra.Command{
		Use:   "dispatch --project <project> --api <URI> [--batch N] [--max-agent N]",
		Short: "Run one dispatch pass: observe in-flight, dispatch queued, escalate",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[dispatchConfig], InitDeps, dispatchHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.DurationVar(&cfg.Rolling, "rolling", 0, "keep dispatching at this interval until interrupted. 0 runs a single pass")
	p := &cfg.Pass
	set.IntVar(&p.Batch, "batch", p.Batch, "max dispatches this pass; 0 = unbounded")
	set.IntVar(&p.MaxAgent, "max-agent", p.MaxAgent, "max agent-stage dispatches this pass; 0 = unbounded")
	set.IntVar(&p.AgentIterations, "agent-iterations", p.AgentIterations, "max agent iterations per session")
	set.StringVar(&p.AgentModel, "agent-model", "", "Gemini model id for agent sessions; empty uses the agent's default")
	set.StringVar(&p.EvalModels, "eval-models", "", "comma-separated Gemini model ids, round-robined across agent launches within a run; overrides --agent-model. For A/B comparison from one saturating loop")
	set.IntVar(&p.Workers, "workers", p.Workers, "concurrent observations and launches within a pass")
	set.IntVar(&p.BuildRate, "build-rate", p.BuildRate, "build launches per minute; 0 launches as fast as the workers allow")
	set.DurationVar(&p.Window, "window", 0, "stop launching this long after the pass starts; 0 launches everything approved")
	set.DurationVar(&p.Refill, "refill", 0, "re-observe and refill this often until --window closes; 0 runs one pass")
	set.DurationVar(&p.BuildTimeCap, "build-time-cap", 0, "build execution time cap per period; 0 = uncapped")
	set.DurationVar(&p.SessionTimeCap, "session-time-cap", 0, "agent session time cap per period; 0 = uncapped")
	set.DurationVar(&p.Period, "period", p.Period, "budget period length")
	set.IntVar(&p.MaxInflightBuilds, "max-inflight-builds", 0, "max concurrent builds in flight; 0 = uncapped")
	set.IntVar(&p.MaxInflightSessions, "max-inflight-sessions", 0, "max concurrent agent sessions in flight; 0 = uncapped")
	set.DurationVar(&p.InflightTimeout, "inflight-timeout", p.InflightTimeout, "requeue dispatches in flight longer than this; 0 = never")
	set.IntVar(&p.MaxRetries, "max-retries", p.MaxRetries, "max same-stage transient retries before stopping for triage")
	set.Float64Var(&p.FreshnessK, "freshness-k", p.FreshnessK, "freshness boost coefficient k in 1+k*exp(-age/tau)")
	set.Float64Var(&p.FreshnessTauHours, "freshness-tau-hours", p.FreshnessTauHours, "freshness decay constant tau in hours")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
