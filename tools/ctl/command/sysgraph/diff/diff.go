// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diff

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/sysgraph/pbdigest"
	sgpb "github.com/google/oss-rebuild/pkg/sysgraph/proto/sysgraph"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgdiff"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgstorage"
	"github.com/google/oss-rebuild/pkg/sysgraph/sgtransform"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the diff command.
type Config struct {
	OldPath     string
	NewPath     string
	Verbose     bool
	JSON        bool
	MaxItems    int
	NoNormalize bool
	Normalize   string
	Quiet       bool
	Root        string
	RootArgv    string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.OldPath == "" || c.NewPath == "" {
		return errors.New("both old and new sysgraph paths are required")
	}
	if c.Normalize != "" {
		for p := range strings.SplitSeq(c.Normalize, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, err := regexp.Compile(p); err != nil {
				return errors.Errorf("invalid normalize pattern %q: %v", p, err)
			}
		}
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

// Handler contains the business logic for diffing two sysgraphs.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	// Load sysgraphs.
	fmt.Fprintf(deps.IO.Err, "Loading old sysgraph from %s...\n", cfg.OldPath)
	oldSg, err := sgstorage.LoadSysGraph(ctx, cfg.OldPath)
	if err != nil {
		return nil, errors.Wrap(err, "loading old sysgraph")
	}
	defer oldSg.Close()

	fmt.Fprintf(deps.IO.Err, "Loading new sysgraph from %s...\n", cfg.NewPath)
	newSg, err := sgstorage.LoadSysGraph(ctx, cfg.NewPath)
	if err != nil {
		return nil, errors.Wrap(err, "loading new sysgraph")
	}
	defer newSg.Close()

	// Filter to subtree if --root or --root-argv is specified.
	var oldFiltered, newFiltered sgtransform.SysGraph = oldSg, newSg
	if cfg.Root != "" || cfg.RootArgv != "" {
		var filterParts []string
		if cfg.Root != "" {
			filterParts = append(filterParts, fmt.Sprintf("exec=%q", cfg.Root))
		}
		if cfg.RootArgv != "" {
			filterParts = append(filterParts, fmt.Sprintf("argv contains %q", cfg.RootArgv))
		}
		filterDesc := strings.Join(filterParts, " AND ")

		makeFilter := func(sg sgtransform.SysGraph) func(ctx context.Context, a *sgpb.Action) bool {
			resources, _ := sg.Resources(ctx)
			return func(ctx context.Context, a *sgpb.Action) bool {
				if cfg.Root != "" {
					digestStr := a.GetExecutableResourceDigest()
					if digestStr == "" {
						return false
					}
					digest, err := pbdigest.NewFromString(digestStr)
					if err != nil {
						return false
					}
					res, ok := resources[digest]
					if !ok || res.GetFileInfo() == nil {
						return false
					}
					path := res.GetFileInfo().GetPath()
					base := path
					if idx := strings.LastIndex(path, "/"); idx >= 0 {
						base = path[idx+1:]
					}
					if base != cfg.Root {
						return false
					}
				}
				if cfg.RootArgv != "" {
					if a.GetExecInfo() == nil {
						return false
					}
					found := false
					for _, arg := range a.GetExecInfo().GetArgv() {
						if strings.Contains(arg, cfg.RootArgv) {
							found = true
							break
						}
					}
					if !found {
						return false
					}
				}
				return true
			}
		}

		fmt.Fprintf(deps.IO.Err, "Filtering to subtree starting from %s...\n", filterDesc)

		oldFiltered, err = sgtransform.FilterForRoot(ctx, oldSg, makeFilter(oldSg))
		if err != nil {
			return nil, errors.Wrapf(err, "finding root (%s) in old sysgraph", filterDesc)
		}

		newFiltered, err = sgtransform.FilterForRoot(ctx, newSg, makeFilter(newSg))
		if err != nil {
			return nil, errors.Wrapf(err, "finding root (%s) in new sysgraph", filterDesc)
		}

		fmt.Fprintf(deps.IO.Err, "Filtered to subtrees\n")
	}

	// Configure options.
	opts := sgdiff.DefaultOptions()
	opts.MaxItemsPerCategory = cfg.MaxItems
	opts.Verbose = cfg.Verbose

	if cfg.NoNormalize {
		opts.NormalizationRules = nil
	}

	if cfg.Normalize != "" {
		for p := range strings.SplitSeq(cfg.Normalize, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, errors.Errorf("invalid normalize pattern %q: %v", p, err)
			}
			opts.NormalizationRules = append(opts.NormalizationRules, &sgdiff.CustomNormalizer{
				Patterns: []*regexp.Regexp{re},
				Desc:     "custom: " + p,
			})
		}
	}

	// Perform diff.
	fmt.Fprintf(deps.IO.Err, "Comparing sysgraphs...\n")
	diff, err := sgdiff.Diff(ctx, oldFiltered, newFiltered, opts)
	if err != nil {
		return nil, errors.Wrap(err, "diffing sysgraphs")
	}

	// Output results.
	if cfg.JSON {
		output, err := json.MarshalIndent(diff, "", "  ")
		if err != nil {
			return nil, errors.Wrap(err, "marshaling JSON")
		}
		fmt.Fprintln(deps.IO.Out, string(output))
	} else if cfg.Quiet {
		fmt.Fprintf(deps.IO.Out, "Summary: %s\n", diff.Summary())
		if len(diff.SecurityAlerts) > 0 {
			fmt.Fprintln(deps.IO.Out, "\nSecurity Alerts:")
			for _, alert := range diff.SecurityAlerts {
				icon := "[!]"
				if alert.Severity == "critical" {
					icon = "[!!]"
				}
				fmt.Fprintf(deps.IO.Out, "  %s %s\n", icon, alert.Description)
			}
		}
		if len(diff.NormalizedCounts) > 0 {
			fmt.Fprintln(deps.IO.Out, "\nNormalized:")
			for reason, count := range diff.NormalizedCounts {
				fmt.Fprintf(deps.IO.Out, "  %d files: %s\n", count, reason)
			}
		}
	} else {
		outputOpts := sgdiff.OutputOptions{
			MaxItemsPerCategory: cfg.MaxItems,
			Verbose:             cfg.Verbose,
		}
		fmt.Fprint(deps.IO.Out, diff.StringWithOptions(outputOpts))
	}

	if len(diff.SecurityAlerts) > 0 {
		return nil, errors.New("security alerts detected")
	}

	return &act.NoOutput{}, nil
}

// Command creates a new diff command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "diff <old-sysgraph> <new-sysgraph>",
		Short: "Compare two sysgraphs and output their differences",
		Long: `Compare two sysgraphs and output their differences.

Sysgraph paths can be:
  - Local directories containing sysgraph files
  - Local .zip files
  - GCS paths (gs://bucket/path)`,
		Args: cobra.ExactArgs(2),
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
	cfg.OldPath = args[0]
	cfg.NewPath = args[1]
	return nil
}

// flagSet returns the command-line flags for the Config struct.
func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.BoolVar(&cfg.Verbose, "v", false, "verbose output (show all items without truncation)")
	set.BoolVar(&cfg.JSON, "json", false, "output as JSON")
	set.IntVar(&cfg.MaxItems, "max-items", 10, "maximum items per category (0 for unlimited)")
	set.BoolVar(&cfg.NoNormalize, "no-normalize", false, "disable normalization (show all file changes)")
	set.StringVar(&cfg.Normalize, "normalize", "", "additional path patterns to normalize (comma-separated regexps)")
	set.BoolVar(&cfg.Quiet, "q", false, "quiet mode (only show security alerts and summary)")
	set.StringVar(&cfg.Root, "root", "", "filter to subtree starting from action with this executable basename")
	set.StringVar(&cfg.RootArgv, "root-argv", "", "filter to subtree starting from action whose argv contains this string")
	return set
}
