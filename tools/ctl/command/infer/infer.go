// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package infer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// Config holds all configuration for the infer command.
type Config struct {
	Ecosystem        string
	Package          string
	Version          string
	Artifact         string
	RepoHint         string
	API              string
	Format           string
	BootstrapBucket  string
	BootstrapVersion string
	GitCacheURL      string
	MemoryLimit      string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Ecosystem == "" {
		return errors.New("ecosystem is required")
	}
	if c.Package == "" {
		return errors.New("package is required")
	}
	if c.Version == "" {
		return errors.New("version is required")
	}
	if c.API != "" && c.GitCacheURL != "" {
		return errors.New("git-cache-url is not supported when using a remote API")
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

func isCloudRun(u *url.URL) bool {
	return strings.HasSuffix(u.Host, ".run.app")
}

// parseMemoryLimit accepts a docker-style size suffix (g/G, m/M, k/K) and
// returns the equivalent number of bytes. Empty input is reported as 0.
func parseMemoryLimit(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSpace(s)
	mult := int64(1)
	switch s[len(s)-1] {
	case 'g', 'G':
		mult = 1 << 30
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'k', 'K':
		mult = 1 << 10
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, errors.Wrapf(err, "parsing memory limit %q", s)
	}
	if n < 0 {
		return 0, errors.New("memory limit must be non-negative")
	}
	return n * mult, nil
}

// Handler contains the business logic for the infer command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	if n, err := parseMemoryLimit(cfg.MemoryLimit); err != nil {
		return nil, err
	} else if n > 0 {
		debug.SetMemoryLimit(n)
	}
	var strategyHint *schema.StrategyOneOf
	if cfg.RepoHint != "" {
		strategyHint = &schema.StrategyOneOf{
			LocationHint: &rebuild.LocationHint{
				Location: rebuild.Location{
					Repo: cfg.RepoHint,
				},
			},
		}
	}
	req := schema.InferenceRequest{
		Ecosystem:    rebuild.Ecosystem(cfg.Ecosystem),
		Package:      cfg.Package,
		Version:      cfg.Version,
		Artifact:     cfg.Artifact,
		StrategyHint: strategyHint,
		// TODO: Add support providing dir and ref hints.
	}
	var stub api.StubFn[schema.InferenceRequest, schema.StrategyOneOf]
	if cfg.API != "" {
		apiURL, err := url.Parse(cfg.API)
		if err != nil {
			return nil, errors.Wrap(err, "parsing API endpoint")
		}
		var client *http.Client
		if isCloudRun(apiURL) {
			// If the api is on Cloud Run, we need to use an authorized client.
			apiURL.Scheme = "https"
			client, err = oauth.AuthorizedUserIDClient(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "creating authorized HTTP client")
			}
		} else {
			client = http.DefaultClient
		}
		stub = api.Stub[schema.InferenceRequest, schema.StrategyOneOf](client, apiURL.JoinPath("/infer"))
	} else {
		var regstub api.StubFn[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse]
		if req.Ecosystem == rebuild.CratesIO {
			err := os.MkdirAll("/tmp/crates-registry-cache", 0o755)
			if err != nil {
				return nil, errors.Wrap(err, "initializing registry cache")
			}
			mgr, err := index.NewIndexManagerFromFS(index.IndexManagerConfig{
				Filesystem:            osfs.New("/tmp/crates-registry-cache"),
				CurrentUpdateInterval: 6 * time.Hour,
				MaxSnapshots:          3,
			})
			if err != nil {
				return nil, errors.Wrap(err, "creating index manager")
			}
			deps := &cratesregistryservice.FindRegistryCommitDeps{
				IndexManager: mgr,
			}
			regstub = api.Local(cratesregistryservice.FindRegistryCommit, deps)
		}
		deps := &inferenceservice.InferDeps{
			HTTPClient: http.DefaultClient,
			GitCache:   nil,
			RepoOptF: func() *gitx.RepositoryOptions {
				return &gitx.RepositoryOptions{
					Worktree: memfs.New(),
					Storer:   gitx.NewInMemoryStorer(),
				}
			},
			CratesRegistryStub: regstub,
		}
		if cfg.GitCacheURL != "" {
			u, err := url.Parse(cfg.GitCacheURL)
			if err != nil {
				return nil, errors.Wrap(err, "parsing git cache URL")
			}
			var idClient, apiClient *http.Client
			if isCloudRun(u) {
				idClient, err = oauth.AuthorizedUserIDClient(ctx)
				if err != nil {
					return nil, errors.Wrap(err, "creating authorized ID client for git cache")
				}
				// Use the same authenticated client for GCS access.
				apiClient = idClient
			} else {
				// For local git_cache instances, use unauthenticated clients.
				idClient = http.DefaultClient
				apiClient = http.DefaultClient
			}
			deps.GitCache = &gitcache.Client{IDClient: idClient, APIClient: apiClient, URL: u}
		}
		stub = api.Local(inferenceservice.Infer, deps)
	}
	resp, err := stub(ctx, req)
	if err != nil {
		if cfg.Format == "strategy-or-status" {
			// Surface the error as its Status proto on stdout.
			// NOTE: Also exit 0 to maintain the json-validity of the output.
			body, mErr := protojson.Marshal(status.Convert(err).Proto())
			if mErr != nil {
				return nil, errors.Wrap(mErr, "encoding inference status")
			}
			fmt.Fprintln(deps.IO.Out, string(body))
			return &act.NoOutput{}, nil
		}
		return nil, err
	}
	s, err := resp.Strategy()
	if err != nil {
		return nil, errors.Wrap(err, "parsing strategy")
	}
	if s == nil {
		return nil, errors.New("no strategy")
	}
	inp := rebuild.Input{Target: rebuild.Target{
		Ecosystem: rebuild.Ecosystem(cfg.Ecosystem),
		Package:   cfg.Package,
		Version:   cfg.Version,
		Artifact:  cfg.Artifact,
	}, Strategy: s}
	resources := build.Resources{
		ToolURLs: map[build.ToolType]string{
			// Ex: https://storage.googleapis.com/google-rebuild-bootstrap-tools/v0.0.0-20251211001310-499b5fb97512/timewarp
			build.TimewarpTool: fmt.Sprintf("https://storage.googleapis.com/%s/%s/timewarp", cfg.BootstrapBucket, cfg.BootstrapVersion),
		},
		BaseImageConfig: build.DefaultBaseImageConfig(),
	}
	var plan *local.DockerBuildPlan
	{
		plan, err = local.NewDockerBuildPlanner().GeneratePlan(ctx, inp, build.PlanOptions{
			UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
			Resources:   resources,
		})
		if err != nil {
			return nil, errors.Wrap(err, "generating plan")
		}
	}
	var buildScript string
	{
		plan, err := local.NewDockerRunPlanner().GeneratePlan(ctx, inp, build.PlanOptions{
			UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
			Resources:   resources,
		})
		if err != nil {
			return nil, errors.Wrap(err, "generating plan")
		}
		buildScript = plan.Script
	}
	switch cfg.Format {
	case "", "strategy", "strategy-or-status":
		enc := json.NewEncoder(deps.IO.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			return nil, errors.Wrap(err, "encoding result")
		}
	case "dockerfile":
		deps.IO.Out.Write([]byte(plan.Dockerfile))
	case "debug-steps":
		args := []string{
			"--name=container",
			"img",
		}
		if plan.Privileged {
			args = append([]string{"--privileged", "-v=/var/run/docker.sock:/var/run/docker.sock"}, args...)
		}
		buildScript := fmt.Sprintf(textwrap.Dedent(`
			#!/usr/bin/env bash
			set -eux
			cat <<'EOS' | docker buildx build --tag=img -
			%s
			EOS
			docker run %s
			`[1:]), plan.Dockerfile, strings.Join(args, " "))
		b := cloudbuild.Build{
			Steps: []*cloudbuild.BuildStep{
				{
					Name:   "gcr.io/cloud-builders/docker",
					Script: buildScript,
				},
			},
		}
		enc := json.NewEncoder(deps.IO.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(b); err != nil {
			return nil, errors.Wrap(err, "encoding build steps")
		}
	case "shell-script":
		deps.IO.Out.Write([]byte(buildScript))
	default:
		return nil, errors.Errorf("Unknown --format type: %s", cfg.Format)
	}
	return &act.NoOutput{}, nil
}

// Command creates a new infer command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "infer --ecosystem <ecosystem> --package <name> --version <version> [--repo-hint <repo>] [--artifact <name>] [--api <URI>] [--format strategy|dockerfile|debug-steps]",
		Short: "Run inference",
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
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem")
	set.StringVar(&cfg.Package, "package", "", "the package name")
	set.StringVar(&cfg.Version, "version", "", "the version of the package")
	set.StringVar(&cfg.Artifact, "artifact", "", "the artifact name")
	set.StringVar(&cfg.RepoHint, "repo-hint", "", "a hint of the repository URL where the package is hosted")
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.StringVar(&cfg.Format, "format", "", "format of the output (strategy|strategy-or-status|dockerfile|debug-steps|shell-script)")
	set.StringVar(&cfg.BootstrapBucket, "bootstrap-bucket", "", "the gcs bucket where bootstrap tools are stored")
	set.StringVar(&cfg.BootstrapVersion, "bootstrap-version", "", "the version of bootstrap tools to use")
	set.StringVar(&cfg.GitCacheURL, "git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
	set.StringVar(&cfg.MemoryLimit, "memory", "", "soft cap on this process's resident memory (e.g. 20g, 8192m). Implemented via Go's runtime/debug.SetMemoryLimit; lets the GC throttle before huge in-memory git clones (memory.NewStorage) take down the host.")
	return set
}
