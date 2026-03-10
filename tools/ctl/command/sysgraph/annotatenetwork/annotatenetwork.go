// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package annotatenetwork

import (
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/proxy/netlog"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the annotate-network command.
type Config struct {
	SysgraphPath string
	NetlogPath   string
	OutputPath   string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.SysgraphPath == "" {
		return errors.New("sysgraph path is required")
	}
	if c.NetlogPath == "" {
		return errors.New("netlog path is required")
	}
	if c.OutputPath == "" {
		return errors.New("output path is required")
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

// Handler contains the business logic for annotating network actions.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	// Load sysgraph.
	diskSG, err := sgstorage.LoadSysGraph(ctx, cfg.SysgraphPath)
	if err != nil {
		return nil, errors.Wrap(err, "loading sysgraph")
	}
	defer diskSG.Close()
	// Read and decode netlog.
	netlogData, err := os.ReadFile(cfg.NetlogPath)
	if err != nil {
		return nil, errors.Wrap(err, "reading netlog file")
	}
	var netlogEntries netlog.NetworkActivityLog
	if err := json.Unmarshal(netlogData, &netlogEntries); err != nil {
		return nil, errors.Wrap(err, "decoding netlog")
	}
	// Create annotated view.
	annotatedSG, err := sgtransform.AnnotateNetwork(ctx, diskSG, netlogEntries.HTTPRequests)
	if err != nil {
		return nil, errors.Wrap(err, "annotating network actions")
	}
	// Write enriched sysgraph.
	if err := sgstorage.Write(ctx, annotatedSG, cfg.OutputPath); err != nil {
		return nil, errors.Wrap(err, "writing enriched sysgraph")
	}
	return &act.NoOutput{}, nil
}

// Command creates a new annotate-network command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "annotate-network --sysgraph <path> --netlog <path> --output <path>",
		Short: "Annotates sysgraph network actions with HTTP metadata from netlog",
		Long: `Loads a sysgraph and a netlog, joins them via source port matching,
and writes HTTP metadata (method, scheme, host, path) into the matching
action's metadata. Outputs an enriched sysgraph.`,
		Args: cobra.NoArgs,
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
	set.StringVar(&cfg.SysgraphPath, "sysgraph", "", "path to input sysgraph .zip (or gs:// URI)")
	set.StringVar(&cfg.NetlogPath, "netlog", "", "path to input netlog.json")
	set.StringVar(&cfg.OutputPath, "output", "", "path to output sysgraph .zip")
	return set
}
