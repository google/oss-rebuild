// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/feed"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type feedConfig struct {
	Project   string
	SignalsDB string
	Events    string
	Settle    time.Duration
}

func (c feedConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.SignalsDB == "" {
		return errors.New("signals-db is required")
	}
	if c.Events == "" {
		return errors.New("events is required")
	}
	return nil
}

func readEvents(ctx context.Context, uri string, deps *Deps) ([]feed.Event, error) {
	if uri == "-" {
		return jsonl.DecodeAll[feed.Event](deps.IO.In)
	}
	fsys, name, err := deps.FSFor(ctx, uri)
	if err != nil {
		return nil, err
	}
	return readJSONL[feed.Event](fsys, name)
}

func feedHandler(ctx context.Context, cfg feedConfig, deps *Deps) (*act.NoOutput, error) {
	events, err := readEvents(ctx, cfg.Events, deps)
	if err != nil {
		return nil, errors.Wrap(err, "reading events")
	}
	fire, err := deps.Firestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	dest, err := billyx.NewResolver().DirFS(ctx, cfg.SignalsDB)
	if err != nil {
		return nil, errors.Wrap(err, "resolving the signal database")
	}
	sdb, cleanup, err := signals.Open(dest)
	if err != nil {
		return nil, errors.Wrap(err, "opening signal database")
	}
	defer cleanup()
	p := &feed.Pass{
		Campaigns: db.NewFirestoreCampaigns(fire),
		Signals:   sdb,
		Registry:  deps.Registry,
		Config:    feed.Config{Settle: cfg.Settle},
		Err:       deps.IO.Err,
		Now:       time.Now().UTC(),
	}
	summary, err := p.Run(ctx, events)
	if err != nil {
		return nil, errors.Wrap(err, "running feed pass")
	}
	fmt.Fprintln(deps.IO.Out, summary)
	return &act.NoOutput{}, nil
}

func feedCommand() *cobra.Command {
	cfg := feedConfig{Settle: feed.Defaults.Settle}
	cmd := &cobra.Command{
		Use:   "feed --project <project> --signals-db <uri> --events <path|->",
		Short: "Admit published releases of tracked packages into the queue",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[feedConfig], InitDeps, feedHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.SignalsDB, "signals-db", "", "URI the signal database publishes under, the tracked set and the source of scores")
	set.StringVar(&cfg.Events, "events", "", "path or gs:// URI of a JSONL file of releases (ecosystem, package, version, artifact, published), or - for stdin")
	set.DurationVar(&cfg.Settle, "settle", cfg.Settle, "hold each release out of dispatch this long after publication")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
