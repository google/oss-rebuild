// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gettrackedpackages

import (
	"context"
	"encoding/json"
	"flag"
	"strconv"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the get-tracked command.
type Config struct {
	Format     string
	Bucket     string
	Generation string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Bucket == "" {
		return errors.New("bucket is required")
	}
	if c.Generation == "" {
		return errors.New("generation is required")
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

func parseArgs(cfg *Config, args []string) error {
	if len(args) != 2 {
		return errors.New("expected exactly 2 arguments: bucket and generation")
	}
	cfg.Bucket = args[0]
	cfg.Generation = args[1]
	return nil
}

// Handler contains the business logic for getting tracked packages.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	gcsClient, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating gcs client")
	}
	obj := gcsClient.Bucket(cfg.Bucket).Object(feed.TrackedPackagesFile)
	gen, err := strconv.ParseInt(cfg.Generation, 10, 64)
	if err != nil {
		return nil, errors.Wrap(err, "parsing generation number")
	}
	idx, err := feed.ReadTrackedIndex(ctx, feed.NewGCSObjectDataSource(obj), gen)
	if err != nil {
		return nil, err
	}
	switch cfg.Format {
	case "", "index":
		enc := json.NewEncoder(deps.IO.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(idx); err != nil {
			return nil, errors.Wrap(err, "encoding tracked package index")
		}
		return &act.NoOutput{}, nil
	case "bench":
		var b benchmark.PackageSet
		for eco, packages := range idx {
			for pkg := range packages {
				b.Packages = append(b.Packages, benchmark.Package{
					Name:      pkg,
					Ecosystem: string(eco),
				})
			}
		}
		enc := json.NewEncoder(deps.IO.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(b); err != nil {
			return nil, errors.Wrap(err, "encoding tracked package benchmark")
		}
		return &act.NoOutput{}, nil
	default:
		return nil, errors.Errorf("unknown --format type: %s", cfg.Format)
	}
}

// Command creates a new get-tracked command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "get-tracked [--format=index|bench] <gcs-bucket> <generation-num>",
		Short: "Get the list of tracked packages",
		Args:  cobra.ExactArgs(2),
		RunE: cli.RunE(
			&cfg,
			parseArgs,
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
	set.StringVar(&cfg.Format, "format", "", "format of the output (index or bench)")
	return set
}
