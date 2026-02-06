// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package getsessions

import (
	"context"
	"encoding/csv"
	"flag"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the get-sessions command.
type Config struct {
	Project string
	RunID   string
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

// Handler contains the business logic for getting sessions.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	dex, err := rundex.NewFirestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to rundex")
	}
	// TOOD: Add support for difference query options here
	sessions, err := dex.FetchSessions(ctx, &rundex.FetchSessionsReq{})
	if err != nil {
		return nil, errors.Wrap(err, "querying for sessions")
	}
	w := csv.NewWriter(deps.IO.Out)
	defer w.Flush()
	for _, s := range sessions {
		if err := w.Write([]string{s.ID, string(s.Target.Ecosystem), s.Target.Package, s.Target.Version, s.Target.Artifact, s.Status, s.StopReason, s.Summary}); err != nil {
			return nil, errors.Wrap(err, "writing CSV")
		}
	}
	return &act.NoOutput{}, nil
}

// Command creates a new get-sessions command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "get-sessions --project <project> [--run <RunID>]",
		Short: "Get a history of sessions",
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
	set.StringVar(&cfg.RunID, "run", "", "the run ID to filter sessions by")
	return set
}
