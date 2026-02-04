// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runone

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	benchrun "github.com/google/oss-rebuild/tools/benchmark/run"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const analyzeMode = schema.ExecutionMode("analyze")

// Config holds all configuration for the run-one command.
type Config struct {
	API               string
	Local             bool
	BootstrapBucket   string
	BootstrapVersion  string
	Ecosystem         string
	Package           string
	Version           string
	Artifact          string
	Strategy          string
	UseNetworkProxy   bool
	UseSyscallMonitor bool
	OverwriteMode     string
	Mode              string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if !c.Local && c.API == "" {
		return errors.New("api is required when not running locally")
	}
	if c.Local && (c.BootstrapBucket == "" || c.BootstrapVersion == "") {
		return errors.New("bootstrap-bucket and bootstrap-version are required when running locally")
	}
	if c.Ecosystem == "" {
		return errors.New("ecosystem is required")
	}
	if c.Package == "" {
		return errors.New("package is required")
	}
	if c.Version == "" {
		return errors.New("version is required")
	}
	if c.Mode == "" {
		return errors.New("mode is required")
	}
	mode := schema.ExecutionMode(c.Mode)
	if mode != schema.AttestMode && mode != analyzeMode {
		return errors.Errorf("unknown mode: %s. Expected one of 'attest', or 'analyze'", c.Mode)
	}
	if c.Local && mode == analyzeMode {
		return errors.New("analyze mode is not supported in local execution")
	}
	if c.OverwriteMode != "" && c.OverwriteMode != string(schema.OverwriteServiceUpdate) && c.OverwriteMode != string(schema.OverwriteForce) {
		return errors.Errorf("invalid overwrite-mode: %s. Expected one of 'SERVICE_UPDATE' or 'FORCE'", c.OverwriteMode)
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
		return errors.New("expected exactly 1 argument: mode")
	}
	cfg.Mode = args[0]
	return nil
}

// Handler contains the business logic for the run-one command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	mode := schema.ExecutionMode(cfg.Mode)
	var strategy *schema.StrategyOneOf
	{
		if cfg.Strategy != "" {
			if mode == schema.AttestMode {
				return nil, errors.New("--strategy not supported in attest mode, use --strategy-from-repo")
			}
			if mode == analyzeMode {
				return nil, errors.New("--strategy not supported in analyze mode")
			}
			f, err := os.Open(cfg.Strategy)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			strategy = &schema.StrategyOneOf{}
			err = yaml.NewDecoder(f).Decode(strategy)
			if err != nil {
				return nil, errors.Wrap(err, "reading strategy file")
			}
		}
	}
	_ = strategy // TODO: use strategy when supported
	enc := json.NewEncoder(deps.IO.Out)
	enc.SetIndent("", "  ")

	if cfg.Local {
		return handleLocal(ctx, cfg, deps, enc)
	}
	return handleRemote(ctx, cfg, deps, enc)
}

func handleLocal(ctx context.Context, cfg Config, deps *Deps, enc *json.Encoder) (*act.NoOutput, error) {
	runID := time.Now().UTC().Format(time.RFC3339)
	store, err := localfiles.AssetStore(runID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temp directory")
	}
	prebuildURL := fmt.Sprintf("https://%s.storage.googleapis.com/%s", cfg.BootstrapBucket, cfg.BootstrapVersion)
	executor := benchrun.NewLocalExecutionService(benchrun.LocalExecutionServiceConfig{
		PrebuildURL: prebuildURL,
		Store:       store,
		LogSink:     deps.IO.Out,
	})
	// Local mode only supports attest (validated in Validate)
	resp, err := executor.RebuildPackage(ctx, schema.RebuildPackageRequest{
		Ecosystem: rebuild.Ecosystem(cfg.Ecosystem),
		Package:   cfg.Package,
		Version:   cfg.Version,
		Artifact:  cfg.Artifact,
	})
	if err != nil {
		return nil, errors.Wrap(err, "running local rebuild")
	}
	if err := enc.Encode(resp); err != nil {
		return nil, errors.Wrap(err, "encoding result")
	}
	return &act.NoOutput{}, nil
}

func handleRemote(ctx context.Context, cfg Config, deps *Deps, enc *json.Encoder) (*act.NoOutput, error) {
	mode := schema.ExecutionMode(cfg.Mode)
	apiURL, err := url.Parse(cfg.API)
	if err != nil {
		return nil, errors.Wrap(err, "parsing API endpoint")
	}
	var client *http.Client
	{
		if strings.Contains(apiURL.Host, "run.app") {
			// If the api is on Cloud Run, we need to use an authorized client.
			apiURL.Scheme = "https"
			client, err = oauth.AuthorizedUserIDClient(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "creating authorized HTTP client")
			}
		} else {
			client = http.DefaultClient
		}
	}
	switch mode {
	case analyzeMode:
		stub := api.Stub[schema.AnalyzeRebuildRequest, api.NoReturn](client, apiURL.JoinPath("analyze"))
		_, err := stub(ctx, schema.AnalyzeRebuildRequest{
			Ecosystem: rebuild.Ecosystem(cfg.Ecosystem),
			Package:   cfg.Package,
			Version:   cfg.Version,
			Artifact:  cfg.Artifact,
		})
		if err != nil {
			return nil, errors.Wrap(err, "running analyze")
		}
		fmt.Fprintln(deps.IO.Out, "Analysis completed successfully")
	case schema.AttestMode:
		stub := api.Stub[schema.RebuildPackageRequest, schema.Verdict](client, apiURL.JoinPath("rebuild"))
		resp, err := stub(ctx, schema.RebuildPackageRequest{
			Ecosystem:         rebuild.Ecosystem(cfg.Ecosystem),
			Package:           cfg.Package,
			Version:           cfg.Version,
			Artifact:          cfg.Artifact,
			UseNetworkProxy:   cfg.UseNetworkProxy,
			UseSyscallMonitor: cfg.UseSyscallMonitor,
			OverwriteMode:     schema.OverwriteMode(cfg.OverwriteMode),
			ID:                time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, errors.Wrap(err, "running attest")
		}
		if err := enc.Encode(resp); err != nil {
			return nil, errors.Wrap(err, "encoding result")
		}
	}
	return &act.NoOutput{}, nil
}

// Command creates a new run-one command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "run-one attest|analyze [--api <URI> | --local --bootstrap-bucket <BUCKET> --bootstrap-version <VERSION>] --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>]",
		Short: "Run a single target",
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
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.BoolVar(&cfg.Local, "local", false, "run locally instead of through the API")
	set.StringVar(&cfg.BootstrapBucket, "bootstrap-bucket", "", "the GCS bucket where bootstrap tools are stored (required for local mode)")
	set.StringVar(&cfg.BootstrapVersion, "bootstrap-version", "", "the version of bootstrap tools to use (required for local mode)")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem")
	set.StringVar(&cfg.Package, "package", "", "the package name")
	set.StringVar(&cfg.Version, "version", "", "the version of the package")
	set.StringVar(&cfg.Artifact, "artifact", "", "the artifact name")
	set.StringVar(&cfg.Strategy, "strategy", "", "the strategy file to use")
	set.BoolVar(&cfg.UseNetworkProxy, "use-network-proxy", false, "request the newtwork proxy")
	set.BoolVar(&cfg.UseSyscallMonitor, "use-syscall-monitor", false, "request syscall monitoring")
	set.StringVar(&cfg.OverwriteMode, "overwrite-mode", "", "reason to overwrite existing attestation (SERVICE_UPDATE or FORCE)")
	return set
}
