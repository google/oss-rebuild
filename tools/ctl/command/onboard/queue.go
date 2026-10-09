// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// enqueue
// ---------------------------------------------------------------------------

type enqueueConfig struct {
	Project           string
	Ecosystem         string
	SignalsDB         string
	FromPackages      string
	MaxVersions       int
	FreshnessK        float64
	FreshnessTauHours float64
}

func (c enqueueConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.Ecosystem == "" {
		return errors.New("ecosystem is required")
	}
	if c.SignalsDB == "" {
		return errors.New("signals-db is required")
	}
	if len(c.packages()) == 0 {
		return errors.New("from-packages is required")
	}
	return nil
}

// packages splits the comma-separated --from-packages list.
func (c enqueueConfig) packages() []string {
	var out []string
	for name := range strings.SplitSeq(c.FromPackages, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func enqueueHandler(ctx context.Context, cfg enqueueConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	campaigns := db.NewFirestoreCampaigns(fire)
	// Use the signal database to source candidates, avoiding consulting the registry.
	sdb, cleanup, err := openSignalDB(ctx, cfg.SignalsDB)
	if err != nil {
		return nil, errors.Wrap(err, "opening signal database")
	}
	defer cleanup()
	now := time.Now().UTC()
	var newPkgs, newVersions, tracked int
	for _, pkg := range cfg.packages() {
		rows, err := signals.VersionSignals(sdb, cfg.Ecosystem, pkg)
		if err != nil {
			return nil, errors.Wrapf(err, "reading versions of %s", pkg)
		}
		if len(rows) == 0 {
			fmt.Fprintf(deps.IO.Err, "skip %s: the signal database ranks no version of it\n", pkg)
			continue
		}
		enqueued, skipped := enqueuePackage(ctx, campaigns, deps.IO.Err, cfg, pkg, rows, now)
		if enqueued > 0 {
			newPkgs++
		}
		newVersions += enqueued
		tracked += skipped
	}
	fmt.Fprintf(deps.IO.Out, "enqueued %d version(s) of %d package(s) (%s); %d already tracked\n", newVersions, newPkgs, cfg.Ecosystem, tracked)
	return &act.NoOutput{}, nil
}

// openSignalDB fetches the published signal database and opens it,
// returning a cleanup that cleans up the connection and the local copy. Each
// invocation will fetch its own temporary copy.
// TODO: Use a shared generation-keyed cache once callers run at job cadence.
func openSignalDB(ctx context.Context, destURI string) (*sqlite3.Conn, func(), error) {
	dest, err := billyx.NewResolver().DirFS(ctx, destURI)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "signals-")
	if err != nil {
		return nil, nil, err
	}
	path, err := signals.Fetch(dest, dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	sdb, err := sqlite3.Open(path)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	return sdb, func() { sdb.Close(); os.RemoveAll(dir) }, nil
}

// enqueuePackage inserts campaigns for a package's ranked versions and
// returns how many were enqueued and how many already existed. The export
// requires PyPI version entries contain artifact names and, currently, only
// pure wheels are included. No other ecosystems require artifact names.
func enqueuePackage(ctx context.Context, campaigns db.Campaigns, errw io.Writer, cfg enqueueConfig, pkg string, rows []signals.VersionSignal, now time.Time) (enqueued, skipped int) {
	eco := rebuild.Ecosystem(cfg.Ecosystem)
	named := make([]signals.VersionSignal, 0, len(rows))
	for _, r := range rows {
		if r.Artifact == "" {
			if eco == rebuild.PyPI {
				fmt.Fprintf(errw, "skip %s@%s: no pure wheel\n", pkg, r.Version)
				continue
			}
			art, err := meta.GuessArtifact(ctx, rebuild.Target{Ecosystem: eco, Package: pkg, Version: r.Version}, rebuild.RegistryMux{})
			if err != nil {
				fmt.Fprintf(errw, "skip %s@%s: resolving artifact: %v\n", pkg, r.Version, err)
				continue
			}
			r.Artifact = art
		}
		named = append(named, r)
	}
	for _, c := range admit(eco, pkg, named, cfg, now) {
		switch err := campaigns.Insert(ctx, c); err {
		case nil:
			enqueued++
		case db.ErrAlreadyExists:
			skipped++
		default:
			fmt.Fprintf(errw, "enqueue %s@%s: %v\n", pkg, c.Version, err)
		}
	}
	return enqueued, skipped
}

// admit turns a package's ranked versions into queue documents, ordered by
// DispatchOrder and cut to --max-versions, so the cap keeps the versions
// most worth rebuilding rather than merely the newest. Admission uses the
// same ordering the queue is drained by, so a version that would never
// reach the front never enters.
func admit(eco rebuild.Ecosystem, pkg string, rows []signals.VersionSignal, cfg enqueueConfig, now time.Time) []scheduler.Campaign {
	out := make([]scheduler.Campaign, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduler.Campaign{
			Ecosystem: string(eco), Package: pkg, Version: r.Version, Artifact: r.Artifact,
			Stage:     scheduler.StageInfer,
			State:     scheduler.StateQueued,
			Score:     r.Prevalence,
			Published: r.Published,
			Updated:   now,
		})
	}
	out = scheduler.Order(out, now, cfg.FreshnessK, cfg.FreshnessTauHours)
	if cfg.MaxVersions > 0 && len(out) > cfg.MaxVersions {
		out = out[:cfg.MaxVersions]
	}
	return out
}

func enqueueCommand() *cobra.Command {
	cfg := enqueueConfig{}
	cmd := &cobra.Command{
		Use:   "enqueue --project <project> --ecosystem <eco> --signals-db <uri> --from-packages <names> [--max-versions N]",
		Short: "Enqueue packages' ranked versions at the infer stage",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[enqueueConfig], InitDeps, enqueueHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem (npm, pypi, cratesio, rubygems)")
	set.StringVar(&cfg.SignalsDB, "signals-db", "", "URI the signal database publishes under, the source of versions and scores")
	set.StringVar(&cfg.FromPackages, "from-packages", "", "comma-separated names of the packages to enqueue")
	set.IntVar(&cfg.MaxVersions, "max-versions", 10, "cap the versions enqueued per package, highest dispatch order first; 0 = all")
	set.Float64Var(&cfg.FreshnessK, "freshness-k", scheduler.DefaultFreshnessK, "freshness boost coefficient k in 1+k*exp(-age/tau)")
	set.Float64Var(&cfg.FreshnessTauHours, "freshness-tau-hours", scheduler.DefaultFreshnessTauHours, "freshness decay constant tau in hours")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

type statusConfig struct {
	Project string
}

func (c statusConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	return nil
}

func statusHandler(ctx context.Context, cfg statusConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	all, err := db.ListCampaigns(ctx, fire)
	if err != nil {
		return nil, errors.Wrap(err, "listing campaigns")
	}
	byState := map[scheduler.State]int{}
	byStage := map[scheduler.Stage]int{}
	var attested int
	for _, c := range all {
		byState[c.State]++
		byStage[c.Stage]++
		if c.Outcome == scheduler.OutcomeAttested || c.State == scheduler.StateDone {
			attested++
		}
	}
	out := deps.IO.Out
	fmt.Fprintf(out, "campaigns: %d\n", len(all))
	fmt.Fprintf(out, "  by state: queued=%d inflight=%d done=%d needs-triage=%d\n",
		byState[scheduler.StateQueued], byState[scheduler.StateInFlight], byState[scheduler.StateDone], byState[scheduler.StateNeedsTriage])
	fmt.Fprintf(out, "  by stage: replay=%d infer=%d agent=%d\n",
		byStage[scheduler.StageReplay], byStage[scheduler.StageInfer], byStage[scheduler.StageAgent])
	if len(all) > 0 {
		fmt.Fprintf(out, "coverage (attested/total): %d/%d = %.1f%%\n", attested, len(all), 100*float64(attested)/float64(len(all)))
	}
	return &act.NoOutput{}, nil
}

func statusCommand() *cobra.Command {
	cfg := statusConfig{}
	cmd := &cobra.Command{
		Use:   "status --project <project>",
		Short: "Print queue state and coverage",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[statusConfig], InitDeps, statusHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
