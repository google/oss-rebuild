// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package annotateinferencediff annotates a manual build.yaml with how it
// differs from a precomputed inference outcome. It does not run inference
// itself; it consumes the output of `ctl infer --format=strategy-or-status`
// and rewrites the file's leading comment block accordingly.
// NOTE: We canonicalize `pip install` commands to make diffs clearer.
package annotateinferencediff

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	billyutil "github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the annotate-inference-diff command.
type Config struct {
	InferencePath string // path to inference output, or "-" for stdin
	DryRun        bool
	Check         bool
	BuildYAML     string // positional: the manual build.yaml to annotate
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.InferencePath == "" {
		return errors.New("--inference is required (path to `ctl infer` output, or '-' for stdin)")
	}
	if c.BuildYAML == "" {
		return errors.New("a build.yaml path is required")
	}
	if c.DryRun && c.Check {
		return errors.New("--dry-run and --check are mutually exclusive (--check already implies dry-run)")
	}
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO  cli.IO
	FS  billy.Filesystem
	CWD string
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps with a filesystem rooted at "/" and CWD set to
// the process working directory at startup.
func InitDeps(context.Context) (*Deps, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errors.Wrap(err, "resolving cwd")
	}
	return &Deps{FS: osfs.New("/"), CWD: cwd}, nil
}

func readInferenceBytes(deps *Deps, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(deps.IO.In)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(deps.CWD, path)
	}
	return billyutil.ReadFile(deps.FS, path)
}

// cleanInferenceMessage peels off the wrapper prefixes that inferenceservice
// and its callers stack on generator errors, leaving just the inner cause.
func cleanInferenceMessage(s string) string {
	s = strings.TrimPrefix(s, "failed to infer strategy: ")
	s = strings.TrimPrefix(s, "[INTERNAL] ")
	return s
}

// errCheckFailed is returned by Handler in --check mode when the manual
// annotation is out of sync with the inference outcome.
var errCheckFailed = errors.New("annotation is out of sync with inference (--check)")

// Handler contains the business logic for the annotate-inference-diff command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	data, err := readInferenceBytes(deps, cfg.InferencePath)
	if err != nil {
		return nil, errors.Wrap(err, "reading inference output")
	}
	// Discriminate Status from StrategyOneOf by trying Status first: protojson
	// rejects unknown fields, so a successful parse with a non-OK code is
	// unambiguously a Status. Everything else falls through to strategy decode.
	var st *status.Status
	var inferredStrategy *schema.StrategyOneOf
	{
		sp := &spb.Status{}
		if err := protojson.Unmarshal(data, sp); err == nil && codes.Code(sp.GetCode()) != codes.OK {
			st = status.FromProto(sp)
		} else {
			inferredStrategy = &schema.StrategyOneOf{}
			if err := json.Unmarshal(data, inferredStrategy); err != nil {
				return nil, errors.Wrap(err, "decoding inference output as strategy or status")
			}
		}
	}
	buildPath := cfg.BuildYAML
	if !filepath.IsAbs(buildPath) {
		buildPath = filepath.Join(deps.CWD, buildPath)
	}
	original, err := billyutil.ReadFile(deps.FS, buildPath)
	if err != nil {
		return nil, errors.Wrap(err, "reading build.yaml")
	}
	stripped := stripExistingHeader(original)

	var newContent []byte
	switch {
	case st != nil:
		reason := cleanInferenceMessage(st.Message())
		header := formatFailsHeader(reason)
		newContent = append([]byte(header), stripped...)
		fmt.Fprintf(deps.IO.Err, "fail %s: %s\n", cfg.BuildYAML, reason)
	case inferredStrategy != nil:
		var def schema.BuildDefinition
		if err := yaml.Unmarshal(stripped, &def); err != nil {
			return nil, errors.Wrap(err, "decoding manual definition")
		}
		if def.StrategyOneOf == nil {
			fmt.Fprintf(deps.IO.Err, "skip %s: no strategy in file\n", cfg.BuildYAML)
			return &act.NoOutput{}, nil
		}
		man, err := def.StrategyOneOf.Strategy()
		if err != nil {
			return nil, errors.Wrap(err, "extracting manual strategy")
		}
		inf, err := inferredStrategy.Strategy()
		if err != nil {
			return nil, errors.Wrap(err, "extracting inferred strategy")
		}
		if man == nil || inf == nil {
			return nil, errors.New("nil strategy in manual or inferred")
		}
		diffs, err := diffStrategies(man, inf)
		if err != nil {
			return nil, errors.Wrap(err, "diffing strategies")
		}
		if len(diffs) == 0 {
			newContent = stripped
			if bytes.Equal(newContent, original) {
				fmt.Fprintf(deps.IO.Err, "match %s\n", cfg.BuildYAML)
			} else {
				fmt.Fprintf(deps.IO.Err, "match %s (stale header removed)\n", cfg.BuildYAML)
			}
		} else {
			header := formatDiffersHeader(diffs)
			newContent = append([]byte(header), stripped...)
			fmt.Fprintf(deps.IO.Err, "diff %s: %d field(s)\n", cfg.BuildYAML, len(diffs))
			for _, d := range diffs {
				body := d.body
				if d.multiline {
					body = strings.ReplaceAll(strings.TrimRight(body, "\n"), "\n", `\n`)
				}
				fmt.Fprintf(deps.IO.Err, "  %s: %s\n", d.field, body)
			}
		}
	}
	hasDiff := !bytes.Equal(newContent, original)
	if cfg.Check {
		if hasDiff {
			return nil, errCheckFailed
		}
		return &act.NoOutput{}, nil
	}
	if cfg.DryRun {
		return &act.NoOutput{}, nil
	}
	if hasDiff {
		if err := billyutil.WriteFile(deps.FS, buildPath, newContent, 0644); err != nil {
			return nil, errors.Wrap(err, "writing build.yaml")
		}
	}
	return &act.NoOutput{}, nil
}

func parseArgs(cfg *Config, args []string) error {
	if len(args) != 1 {
		return errors.New("expected exactly one build.yaml path")
	}
	cfg.BuildYAML = args[0]
	return nil
}

// Command creates a new annotate-inference-diff command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "annotate-inference-diff --inference <file|-> [--dry-run|--check] <build.yaml>",
		Short: "Annotate a build.yaml with how it differs from a precomputed inference outcome",
		Long: "Compares a precomputed inference outcome against the manual strategy in <build.yaml> " +
			"and rewrites its leading comment block: a diff (success) or a failure reason (failure). " +
			"The output of `ctl infer --format=strategy-or-status` must be provided as `--inference`.",
		Args: cobra.ExactArgs(1),
		RunE: cli.RunE(&cfg, parseArgs, InitDeps, Handler),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.InferencePath, "inference", "", "path to `ctl infer` output (a StrategyOneOf JSON on success, or a google.rpc.Status protojson on failure); use '-' to read from stdin")
	set.BoolVar(&cfg.DryRun, "dry-run", false, "print the diff without modifying the file")
	set.BoolVar(&cfg.Check, "check", false, "exit non-zero if the file would be modified; useful as a CI gate to catch undocumented changes")
	return set
}
