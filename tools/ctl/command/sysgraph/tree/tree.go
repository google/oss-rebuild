// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tree

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the tree command.
type Config struct {
	SysgraphPath string
	RootID       int64 // filter to subtree by action ID
	AncestorID   int64 // show ancestor path to this action ID
	MaxDepth     int   // limit recursion (0 = unlimited)
	Collapse     int   // group N+ siblings with same exec (default 10)
	ShowForks    bool  // include fork-only actions
	ShowFiles    bool  // show file reads/writes per process
	Verbose      bool  // show ids, cwd, and duration
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.SysgraphPath == "" {
		return errors.New("sysgraph path is required")
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

// Handler contains the business logic for the tree command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	sg, err := sgstorage.LoadSysGraph(ctx, cfg.SysgraphPath)
	if err != nil {
		return nil, fmt.Errorf("loading sysgraph: %w", err)
	}
	defer sg.Close()

	var filtered sgtransform.SysGraph = sg
	if cfg.RootID != 0 {
		filtered, err = sgtransform.SubgraphForRoots(ctx, sg, []int64{cfg.RootID})
		if err != nil {
			return nil, fmt.Errorf("filtering for root: %w", err)
		}
	}

	tree, err := buildTree(ctx, filtered)
	if err != nil {
		return nil, fmt.Errorf("building tree: %w", err)
	}

	var resources map[pbdigest.Digest]*sgpb.Resource
	if cfg.ShowFiles {
		resources, err = filtered.Resources(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading resources: %w", err)
		}
	}

	opts := renderOpts{
		MaxDepth:     cfg.MaxDepth,
		Collapse:     cfg.Collapse,
		ShowForks:    cfg.ShowForks,
		ShowIDs:      cfg.Verbose,
		ShowCwd:      cfg.Verbose,
		ShowDuration: cfg.Verbose,
		ShowFiles:    cfg.ShowFiles,
		AncestorID:   cfg.AncestorID,
		resources:    resources,
	}
	renderTree(deps.IO.Out, tree, opts)
	return &act.NoOutput{}, nil
}

// Command creates a new tree command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "tree <sysgraph>",
		Short: "Show process tree trace of a build",
		Long: `Displays a bash-like process tree trace of the build, showing what
commands were executed and in what order. Each nesting level is indicated
by a '+' prefix (similar to bash set -x).

Fork-only actions are collapsed by default, promoting their exec'd children
up to the parent depth. Consecutive siblings with the same executable are
grouped when there are many (controlled by --collapse).`,
		Args: cobra.ExactArgs(1),
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

func parseArgs(cfg *Config, args []string) error {
	cfg.SysgraphPath = args[0]
	return nil
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.Int64Var(&cfg.RootID, "root-id", 0, "filter to subtree starting from this action ID (from --ids output)")
	set.Int64Var(&cfg.AncestorID, "ancestor-id", 0, "show the ancestor path from root down to this action ID (from --ids output)")
	set.IntVar(&cfg.MaxDepth, "max-depth", 0, "limit recursion depth (0 = unlimited)")
	set.IntVar(&cfg.Collapse, "collapse", 0, "group N+ consecutive siblings with same executable (0 = disable)")
	set.BoolVar(&cfg.ShowForks, "show-forks", false, "include fork-only actions in output")
	set.BoolVar(&cfg.ShowFiles, "show-files", false, "show file reads and writes for each process")
	set.BoolVar(&cfg.Verbose, "v", false, "verbose output (show ids, cwd, and duration)")
	return set
}
