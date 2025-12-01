// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package getgradlegav

import (
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/tools/ctl/gradle"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the get-gradle-gav command.
type Config struct {
	Repository string
	Ref        string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Repository == "" {
		return errors.New("repository is required")
	}
	if c.Ref == "" {
		return errors.New("ref is required")
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

// Handler contains the business logic for extracting GAV coordinates.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	tempDir, err := os.MkdirTemp("", "gradle-gav-")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temp directory")
	}
	defer os.RemoveAll(tempDir)
	repo, err := git.PlainClone(tempDir, false, &git.CloneOptions{URL: cfg.Repository})
	if err != nil {
		return nil, errors.Wrap(err, "failed to clone repository")
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get worktree")
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(cfg.Ref)}); err != nil {
		return nil, errors.Wrap(err, "failed to checkout commit")
	}
	gradleProject, err := gradle.RunPrintCoordinates(ctx, *repo, local.NewRealCommandExecutor())
	if err != nil {
		return nil, errors.Wrap(err, "running printCoordinates")
	}
	encoder := json.NewEncoder(deps.IO.Out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(gradleProject); err != nil {
		return nil, errors.Wrap(err, "encoding output")
	}
	return &act.NoOutput{}, nil
}

// Command creates a new get-gradle-gav command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "get-gradle-gav --repository <URI> --ref <ref>",
		Short: "Extracts GAV coordinates from a Gradle project at a given commit",
		Long: `Extracts GAV (Group, Artifact, Version) coordinates from a Gradle project
at a specific Git commit. This command clones the repository, checks out the
specified commit, and runs Gradle to extract the project coordinates.`,
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
	set.StringVar(&cfg.Repository, "repository", "", "the repository URI")
	// TODO: Does not currently support branch or tag.
	set.StringVar(&cfg.Ref, "ref", "", "the git reference (branch, tag, commit)")
	return set
}
