// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"context"
	"flag"
	"log"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/ctl/migrations"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
)

// Config holds all configuration for the migrate command.
type Config struct {
	Project       string
	DryRun        bool
	MigrationName string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.MigrationName == "" {
		return errors.New("migration-name is required")
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
		return errors.New("expected exactly 1 argument: migration-name")
	}
	cfg.MigrationName = args[0]
	return nil
}

// Handler contains the business logic for running migrations.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	client, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	migration, ok := migrations.All[cfg.MigrationName]
	if !ok {
		return nil, errors.Errorf("unknown migration: %s", cfg.MigrationName)
	}
	q := client.CollectionGroup(migration.CollectionGroup).Query
	bw := client.BulkWriter(ctx)
	var total, updated int
	{
		ag := q.NewAggregationQuery()
		ag = ag.WithCount("total-count")
		res, err := ag.Get(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "getting count")
		}
		totalV, ok := res["total-count"].(*firestorepb.Value)
		if !ok {
			return nil, errors.Errorf("couldn't get total count: %+v", res)
		}
		total = int(totalV.GetIntegerValue())
	}
	iter := q.Documents(ctx)
	bar := pb.New(total)
	bar.Output = deps.IO.Err
	bar.ShowTimeLeft = true
	bar.Start()
	defer bar.Finish()
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		bar.Increment()
		if err != nil {
			return nil, errors.Wrap(err, "iterating over attempts")
		}
		updates, err := migration.Transform(doc)
		if errors.Is(err, migrations.ErrSkip) {
			continue
		} else if err != nil {
			return nil, errors.Wrap(err, "transforming field")
		}
		updated++
		if !cfg.DryRun {
			if _, err := bw.Update(doc.Ref, updates); err != nil {
				return nil, errors.Wrap(err, "updating field")
			}
		}
	}
	bar.Finish()
	bw.End()
	log.Printf("Updated %d/%d entries (%2.1f%%)", updated, total, 100.*float64(updated)/float64(total))
	return &act.NoOutput{}, nil
}

// Command creates a new migrate command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "migrate --project <project> [--dryrun] <migration-name>",
		Short: "Migrate firestore entries",
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
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.BoolVar(&cfg.DryRun, "dryrun", false, "true if this migration is a dryrun")
	return set
}
