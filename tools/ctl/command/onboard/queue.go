// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"io"
	"iter"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// enqueue
// ---------------------------------------------------------------------------

type enqueueConfig struct {
	Project           string
	Ecosystem         string
	Prevalence        string
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
	if c.Prevalence == "" {
		return errors.New("prevalence is required")
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
	// The export is the only source of candidates: each is a ranked version
	// carrying its publish time and, for PyPI, its wheel, so admission never
	// consults a registry. A version the export does not rank waits for an
	// export that does.
	prevFS, prev, err := billyx.NewResolver().FS(ctx, cfg.Prevalence)
	if err != nil {
		return nil, errors.Wrap(err, "resolving prevalence export")
	}
	r, err := prevFS.Open(prev)
	if err != nil {
		return nil, errors.Wrap(err, "opening prevalence export")
	}
	defer r.Close()
	pkgs := cfg.packages()
	ranked, err := rankedVersions(jsonl.Decode[signals.PrevalenceRecord](r), cfg.Ecosystem, pkgs)
	if err != nil {
		return nil, errors.Wrap(err, "reading prevalence export")
	}
	now := time.Now().UTC()
	var newPkgs, newVersions, tracked int
	for _, pkg := range pkgs {
		if len(ranked[pkg]) == 0 {
			fmt.Fprintf(deps.IO.Err, "skip %s: the export ranks no version of it\n", pkg)
			continue
		}
		enqueued, skipped := enqueuePackage(ctx, campaigns, deps.IO.Err, cfg, pkg, ranked[pkg], now)
		if enqueued > 0 {
			newPkgs++
		}
		newVersions += enqueued
		tracked += skipped
	}
	fmt.Fprintf(deps.IO.Out, "enqueued %d version(s) of %d package(s) (%s); %d already tracked\n", newVersions, newPkgs, cfg.Ecosystem, tracked)
	return &act.NoOutput{}, nil
}

// rankedVersions collects the named packages' version rows from the export
// in one pass, keyed by package. A package the export ranks no version of
// has no entry.
func rankedVersions(recs iter.Seq2[signals.PrevalenceRecord, error], ecosystem string, pkgs []string) (map[string][]signals.VersionSignal, error) {
	want := make(map[string]bool, len(pkgs))
	for _, pkg := range pkgs {
		want[pkg] = true
	}
	out := map[string][]signals.VersionSignal{}
	for r, err := range recs {
		if err != nil {
			return nil, err
		}
		if r.Ecosystem != ecosystem || r.Version == "" || !want[r.Package] {
			continue
		}
		out[r.Package] = append(out[r.Package], signals.VersionSignal{
			Version: r.Version, Prevalence: r.Prevalence, Published: r.Published, Artifact: r.Artifact,
		})
	}
	return out, nil
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
	order := func(c scheduler.Campaign) float64 {
		return c.DispatchOrder(now, cfg.FreshnessK, cfg.FreshnessTauHours)
	}
	// Ties go newest first.
	sort.SliceStable(out, func(i, j int) bool {
		if oi, oj := order(out[i]), order(out[j]); oi != oj {
			return oi > oj
		}
		return out[i].Published.After(out[j].Published)
	})
	if cfg.MaxVersions > 0 && len(out) > cfg.MaxVersions {
		out = out[:cfg.MaxVersions]
	}
	return out
}

func enqueueCommand() *cobra.Command {
	cfg := enqueueConfig{}
	cmd := &cobra.Command{
		Use:   "enqueue --project <project> --ecosystem <eco> --prevalence <uri> --from-packages <names> [--max-versions N]",
		Short: "Enqueue packages' ranked versions at the infer stage",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[enqueueConfig], InitDeps, enqueueHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem (npm, pypi, cratesio, rubygems)")
	set.StringVar(&cfg.Prevalence, "prevalence", "", "prevalence JSONL export path or gs:// URI, the source of versions and scores")
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
