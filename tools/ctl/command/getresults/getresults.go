// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package getresults

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the get-results command.
type Config struct {
	Project          string
	Run              string
	Bench            string
	Prefix           string
	Pattern          string
	Sample           int
	Format           string
	Clean            bool
	LatestPerPackage bool
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Run == "" {
		return errors.New("run is required")
	}
	if c.Format != "" && c.Format != "summary" && c.Format != "bench" && c.Format != "csv" {
		return errors.Errorf("invalid format: %s. Expected one of 'summary', 'bench', or 'csv'", c.Format)
	}
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) {
	return &Deps{}, nil
}

func buildFetchRebuildRequest(bench, run, prefix, pattern string, clean, latestPerPackage bool) (*rundex.FetchRebuildRequest, error) {
	var runs []string
	if run != "" {
		runs = strings.Split(run, ",")
	}
	req := rundex.FetchRebuildRequest{
		Runs: runs,
		Opts: rundex.FetchRebuildOpts{
			Prefix:  prefix,
			Pattern: pattern,
			Clean:   clean,
		},
	}
	if len(req.Runs) == 0 {
		return nil, errors.New("'run' must be supplied")
	}
	// Load the benchmark, if provided.
	if bench != "" {
		log.Printf("Extracting benchmark %s...\n", filepath.Base(bench))
		set, err := benchmark.ReadBenchmark(bench)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		req.Bench = &set
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	return &req, nil
}

// Handler contains the business logic for the get-results command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	req, err := buildFetchRebuildRequest(cfg.Bench, cfg.Run, cfg.Prefix, cfg.Pattern, cfg.Clean, true)
	if err != nil {
		return nil, err
	}
	var dex rundex.Reader
	if cfg.Project == "" {
		dex = rundex.NewFilesystemClient(localfiles.Rundex())
	} else {
		dex, err = rundex.NewFirestore(ctx, cfg.Project)
		if err != nil {
			return nil, err
		}
	}
	log.Printf("Querying results for [executors=%v,runs=%v,bench=%s,prefix=%s,pattern=%s]", req.Executors, req.Runs, cfg.Bench, req.Opts.Prefix, req.Opts.Pattern)
	rebuilds, err := dex.FetchRebuilds(ctx, req)
	if err != nil {
		return nil, err
	}
	log.Printf("Fetched %d rebuilds", len(rebuilds))
	byCount := rundex.GroupRebuilds(rebuilds)
	if len(byCount) == 0 {
		log.Println("No results")
		return &act.NoOutput{}, nil
	}
	switch cfg.Format {
	case "", "summary":
		log.Println("Verdict summary:")
		for _, vg := range byCount {
			fmt.Fprintf(deps.IO.Out, " %4d - %s (example: %s)\n", vg.Count, vg.Msg[:min(len(vg.Msg), 1000)], vg.Examples[0].ID())
		}
		successes := 0
		for _, r := range rebuilds {
			if r.Success {
				successes++
			}
		}
		fmt.Fprintf(deps.IO.Out, "%d succeeded of %d  (%2.1f%%)\n", successes, len(rebuilds), 100.*float64(successes)/float64(len(rebuilds)))
	case "bench":
		var ps benchmark.PackageSet
		if cfg.Sample > 0 && cfg.Sample < len(rebuilds) {
			ps.Count = cfg.Sample
		} else {
			ps.Count = len(rebuilds)
		}
		rng := rand.New(rand.NewSource(int64(ps.Count)))
		var rbs []rundex.Rebuild
		for _, r := range rebuilds {
			rbs = append(rbs, r)
		}
		slices.SortFunc(rbs, func(a rundex.Rebuild, b rundex.Rebuild) int { return strings.Compare(a.ID(), b.ID()) })
		rng.Shuffle(len(rbs), func(i int, j int) {
			rbs[i], rbs[j] = rbs[j], rbs[i]
		})
		for _, r := range rbs[:ps.Count] {
			idx := -1
			for i, psp := range ps.Packages {
				if psp.Name == r.Package {
					idx = i
					break
				}
			}
			if idx == -1 {
				ps.Packages = append(ps.Packages, benchmark.Package{Name: r.Package, Ecosystem: r.Ecosystem})
				idx = len(ps.Packages) - 1
			}
			ps.Packages[idx].Versions = append(ps.Packages[idx].Versions, r.Version)
			if r.Artifact != "" {
				ps.Packages[idx].Artifacts = append(ps.Packages[idx].Artifacts, r.Artifact)
			}
		}
		ps.Updated = time.Now()
		b, err := json.MarshalIndent(ps, "", "  ")
		if err != nil {
			return nil, errors.Wrap(err, "marshalling benchmark")
		}
		fmt.Fprintln(deps.IO.Out, string(b))
	case "csv":
		w := csv.NewWriter(deps.IO.Out)
		defer w.Flush()
		for _, r := range rebuilds {
			if err := w.Write([]string{r.Ecosystem, r.Package, r.Version, r.Artifact, r.RunID, r.Message}); err != nil {
				return nil, errors.Wrap(err, "writing CSV")
			}
		}
	default:
		return nil, errors.Errorf("Unknown --format type: %s", cfg.Format)
	}
	return &act.NoOutput{}, nil
}

// Command creates a new get-results command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "get-results -project <ID> -run <ID> [-bench <benchmark.json>] [-prefix <prefix>] [-pattern <regex>] [-sample N] [-format=summary|bench|csv]",
		Short: "Analyze rebuild results",
		Args:  cobra.NoArgs,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			Handler,
		),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

// flagSet returns the command-line flags for the Config struct.
func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.StringVar(&cfg.Run, "run", "", "the run(s) from which to fetch results")
	set.StringVar(&cfg.Bench, "bench", "", "a path to a benchmark file for filtering")
	set.StringVar(&cfg.Prefix, "prefix", "", "filter results to those matching this prefix")
	set.StringVar(&cfg.Pattern, "pattern", "", "filter results to those matching this regex pattern")
	set.IntVar(&cfg.Sample, "sample", -1, "if provided, only N results will be displayed")
	set.StringVar(&cfg.Format, "format", "", "format of the output (summary|bench|csv)")
	set.BoolVar(&cfg.Clean, "clean", false, "whether to apply normalization heuristics to group similar verdicts")
	return set
}
