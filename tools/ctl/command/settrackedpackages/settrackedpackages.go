// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package settrackedpackages

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"log"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the set-tracked command.
type Config struct {
	BenchmarkPath string
	Bucket        string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.BenchmarkPath == "" {
		return errors.New("bench is required")
	}
	if c.Bucket == "" {
		return errors.New("bucket is required")
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
	if len(args) != 1 {
		return errors.New("expected exactly 1 argument: bucket")
	}
	cfg.Bucket = args[0]
	return nil
}

func logFailure(f func() error) {
	if err := f(); err != nil {
		log.Println(err)
	}
}

// Handler contains the business logic for setting tracked packages.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	set, err := benchmark.ReadBenchmark(cfg.BenchmarkPath)
	if err != nil {
		return nil, errors.Wrap(err, "reading benchmark file")
	}
	packageMap := make(feed.TrackedPackageIndex)
	for _, p := range set.Packages {
		eco := rebuild.Ecosystem(p.Ecosystem)
		if _, ok := packageMap[eco]; !ok {
			packageMap[eco] = make(map[string]bool)
		}
		packageMap[eco][p.Name] = true
	}
	data := make(feed.TrackedPackageSet)
	for ecoStr, packages := range packageMap {
		ecosystem := rebuild.Ecosystem(ecoStr)
		data[ecosystem] = make([]string, 0, len(packages))
		for pkg := range packages {
			data[ecosystem] = append(data[ecosystem], pkg)
		}
	}
	gcsClient, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating gcs client")
	}
	obj := gcsClient.Bucket(cfg.Bucket).Object(feed.TrackedPackagesFile)
	w := obj.NewWriter(ctx)
	defer logFailure(w.Close)
	gzw := gzip.NewWriter(w)
	defer logFailure(gzw.Close)
	if err := json.NewEncoder(gzw).Encode(data); err != nil {
		return nil, errors.Wrap(err, "compressing and uploading tracked packages")
	}
	return &act.NoOutput{}, nil
}

// Command creates a new set-tracked command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "set-tracked --bench <benchmark.json> <gcs-bucket>",
		Short: "Set the list of tracked packages",
		Args:  cobra.ExactArgs(1),
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
	set.StringVar(&cfg.BenchmarkPath, "bench", "", "a path to a benchmark file")
	return set
}
