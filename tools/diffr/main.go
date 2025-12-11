// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/diffr"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the diffr command.
type Config struct {
	File1      string
	File2      string
	MaxDepth   int
	JSONOutput bool
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.File1 == "" {
		return errors.New("file1 is required")
	}
	if c.File2 == "" {
		return errors.New("file2 is required")
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

var ErrDiffFound = errors.New("files differ")

// Handler contains the business logic for the diffr command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	// Open file1
	f1, err := os.Open(cfg.File1)
	if err != nil {
		return nil, errors.Wrapf(err, "opening file1: %s", cfg.File1)
	}
	defer f1.Close()
	// Open file2
	f2, err := os.Open(cfg.File2)
	if err != nil {
		return nil, errors.Wrapf(err, "opening file2: %s", cfg.File2)
	}
	defer f2.Close()
	// Setup diff options
	opts := diffr.Options{MaxDepth: cfg.MaxDepth}
	if cfg.JSONOutput {
		opts.OutputJSON = deps.IO.Out
	} else {
		opts.Output = deps.IO.Out
	}
	// Run the diff
	err = diffr.Diff(ctx, diffr.File{
		Name:   cfg.File1,
		Reader: f1,
	}, diffr.File{
		Name:   cfg.File2,
		Reader: f2,
	}, opts)
	if errors.Is(err, diffr.ErrNoDiff) {
		return &act.NoOutput{}, nil
	} else if err != nil {
		return nil, err
	} else {
		return nil, ErrDiffFound
	}
}

// ParseArgs parses positional arguments into the Config.
func ParseArgs(cfg *Config, args []string) error {
	if len(args) != 2 {
		return errors.Errorf("expected exactly 2 arguments (file1 file2), got %d", len(args))
	}
	cfg.File1 = args[0]
	cfg.File2 = args[1]
	return nil
}

// Command creates a new diffr command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "diffr [flags] <file1> <file2>",
		Short: "Compare two files recursively through archives",
		Long: `Compare two files recursively through archives.

diffr is a tool for comparing two files, with support for recursively
descending into archives (zip, tar, gzip) to identify differences at
any depth. It can output differences as human-readable text or JSON.

Examples:
  # Compare two zip files
  diffr file1.zip file2.zip

  # Compare with JSON output
  diffr --json file1.tar.gz file2.tar.gz

  # Limit archive recursion depth
  diffr --max-depth 2 file1.zip file2.zip`,
		Args: cobra.ExactArgs(2),
		RunE: cli.RunE(
			&cfg,
			ParseArgs,
			InitDeps,
			Handler,
		),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

// flagSet returns the command-line flags for the Config struct.
func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.IntVar(&cfg.MaxDepth, "max-depth", 0, "maximum archive nesting depth to recurse into (0 = unlimited)")
	set.BoolVar(&cfg.JSONOutput, "json", false, "output diff in JSON format instead of text")
	return set
}

func main() {
	cmd := Command()
	if err := cmd.Execute(); err != nil {
		if errors.Is(err, ErrDiffFound) {
			os.Exit(1)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(127)
		}
	}
}
