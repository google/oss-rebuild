// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package listruns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the list-runs command.
type Config struct {
	Project       string
	BenchmarkPath string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
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

// Handler contains the business logic for listing runs.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	var opts rundex.FetchRunsOpts
	if cfg.BenchmarkPath != "" {
		log.Printf("Extracting benchmark %s...\n", filepath.Base(cfg.BenchmarkPath))
		set, err := benchmark.ReadBenchmark(cfg.BenchmarkPath)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
		opts.BenchmarkHash = hex.EncodeToString(set.Hash(sha256.New()))
	}
	client, err := rundex.NewFirestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	runs, err := client.FetchRuns(ctx, opts)
	if err != nil {
		return nil, errors.Wrap(err, "fetching runs")
	}
	var count int
	for _, r := range runs {
		fmt.Fprintf(deps.IO.Out, "  %s [bench=%s hash=%s]\n", r.ID, r.BenchmarkName, r.BenchmarkHash)
		count++
	}
	switch count {
	case 0:
		fmt.Fprintln(deps.IO.Out, "No results found")
	case 1:
		fmt.Fprintln(deps.IO.Out, "1 result found")
	default:
		fmt.Fprintf(deps.IO.Out, "%d results found\n", count)
	}
	return &act.NoOutput{}, nil
}

// Command creates a new list-runs command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "list-runs --project <ID> [--bench <benchmark.json>]",
		Short: "List runs",
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
	set.StringVar(&cfg.BenchmarkPath, "bench", "", "a path to a benchmark file for filtering")
	return set
}
