// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
	"google.golang.org/api/serviceusage/v1"
	"google.golang.org/genai"
)

const vertexAIService = "aiplatform.googleapis.com"

// Config holds all configuration for the tui command.
type Config struct {
	Project          string
	DebugStorage     string
	LogsBucket       string
	MetadataBucket   string
	BenchmarkDir     string
	Clean            bool
	DefDir           string
	LLMProject       string
	RundexGCSPath    string
	SharedAssetStore string
	BootstrapBucket  string
	BootstrapVersion string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	// All fields are optional for tui command
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

// Handler contains the business logic for the tui command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	if cfg.DebugStorage != "" {
		u, err := url.Parse(cfg.DebugStorage)
		if err != nil {
			return nil, errors.Wrap(err, "parsing --debug-storage as url")
		}
		if u.Scheme == "gs" {
			prefix := strings.TrimPrefix(u.Path, "/")
			if prefix != "" {
				return nil, errors.Errorf("--debug-storage cannot have additional path elements, found %s", prefix)
			}
		}
	}
	var dex rundex.Reader
	var watcher rundex.Watcher
	{
		if cfg.RundexGCSPath != "" {
			u, err := url.Parse(cfg.RundexGCSPath)
			if err != nil {
				return nil, errors.Wrap(err, "parsing --rundex-gcs-path")
			}
			if u.Scheme != "gs" {
				return nil, errors.New("--rundex-gcs-path must be a gs:// URL")
			}
			ctxWithOpts := context.WithValue(ctx, rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
			gcsClient, err := gcs.NewClient(ctxWithOpts)
			if err != nil {
				return nil, errors.Wrap(err, "creating GCS client")
			}
			dex = rundex.NewGCSClient(ctxWithOpts, gcsClient, u.Host, strings.TrimPrefix(u.Path, "/"))
			// GCS watcher is not implemented.
		} else if cfg.Project != "" {
			var err error
			dex, err = rundex.NewFirestore(ctx, cfg.Project)
			if err != nil {
				return nil, err
			}
		} else {
			lc := rundex.NewLocalClient(localfiles.Rundex())
			dex = lc
			watcher = lc
		}
	}
	var buildDefs *rebuild.FilesystemAssetStore
	if cfg.DefDir != "" {
		if fs, err := osfs.New("/").Chroot(cfg.DefDir); err != nil {
			return nil, errors.Wrap(err, "creating asset store in build def dir")
		} else {
			buildDefs = rebuild.NewFilesystemAssetStore(fs)
		}
	} else {
		var err error
		if buildDefs, err = localfiles.BuildDefs(); err != nil {
			return nil, errors.Wrap(err, "failed to create local build def asset store")
		}
	}
	mux := meta.NewRegistryMux(http.DefaultClient)
	var assetStoreFn func(runID string) (rebuild.LocatableAssetStore, error)
	if cfg.SharedAssetStore != "" {
		u, err := url.Parse(cfg.SharedAssetStore)
		if err != nil {
			return nil, errors.Wrap(err, "parsing --merged-asset-store")
		}
		// TODO: Add support for local filesystem based merged-asset-store
		if u.Scheme != "gs" {
			return nil, errors.New("--merged-asset-store currently only supports gs:// URLs")
		}
		assetStoreFn = func(runID string) (rebuild.LocatableAssetStore, error) {
			frontline, err := localfiles.AssetStore(runID)
			if err != nil {
				return nil, err
			}
			ctxWithRunID := context.WithValue(ctx, rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
			ctxWithRunID = context.WithValue(ctxWithRunID, rebuild.RunID, runID)
			backline, err := rebuild.NewGCSStore(ctxWithRunID, cfg.SharedAssetStore)
			if err != nil {
				return nil, err
			}
			return rebuild.NewCachedAssetStore(frontline, backline), nil
		}
	} else {
		assetStoreFn = localfiles.AssetStore
	}
	debug := cfg.DebugStorage
	if debug == "" {
		debug = "file://" + localfiles.AssetsPath()
	}
	butler := localfiles.NewButler(cfg.MetadataBucket, cfg.LogsBucket, debug, mux, assetStoreFn)
	var aiClient *genai.Client
	{
		aiProject := cfg.Project
		if cfg.LLMProject != "" {
			aiProject = cfg.LLMProject
		}
		if aiProject != "" {
			serviceUsageClient, err := serviceusage.NewService(ctx, option.WithScopes(serviceusage.CloudPlatformScope))
			if err != nil {
				return nil, errors.Wrap(err, "failed to create Service Usage client")
			}
			if service, err := serviceUsageClient.Services.Get(fmt.Sprintf("projects/%s/services/%s", aiProject, vertexAIService)).Do(); err != nil {
				return nil, errors.Wrapf(err, "failed to check for vertex AI service")
			} else if service.State == "ENABLED" {
				aiClient, err = genai.NewClient(ctx, &genai.ClientConfig{
					Backend:  genai.BackendVertexAI,
					Project:  aiProject,
					Location: "us-central1",
				})
				if err != nil {
					return nil, errors.Wrap(err, "failed to create a genai client")
				}
			}
		}
	}
	benches := benchmark.NewFSRepository(osfs.New(cfg.BenchmarkDir))
	prebuildConfig := rebuild.PrebuildConfig{Bucket: cfg.BootstrapBucket, Dir: cfg.BootstrapVersion}
	tapp := ide.NewTuiApp(dex, watcher, rundex.FetchRebuildOpts{Clean: cfg.Clean}, benches, buildDefs, butler, aiClient, prebuildConfig)
	if err := tapp.Run(ctx); err != nil {
		// TODO: This cleanup will be unnecessary once NewTuiApp does split logging.
		log.Default().SetOutput(os.Stdout)
		return nil, err
	}
	return &act.NoOutput{}, nil
}

// Command creates a new tui command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "tui [--project <ID>] [--debug-storage <bucket>] [--benchmark-dir <dir>] [--clean] [--llm-project] [--rundex-gcs-path <path>] [--merged-asset-store <path>] [-bootstrap-bucket <BUCKET> -bootstrap-version <VERSION>]",
		Short: "A terminal UI for the OSS-Rebuild debugging tools",
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
	set.StringVar(&cfg.DebugStorage, "debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	set.StringVar(&cfg.LogsBucket, "logs-bucket", "", "the gcs bucket where gcb logs are stored")
	set.StringVar(&cfg.MetadataBucket, "metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	set.StringVar(&cfg.BenchmarkDir, "benchmark-dir", "", "a directory with benchmarks to work with")
	set.BoolVar(&cfg.Clean, "clean", false, "whether to apply normalization heuristics to group similar verdicts")
	set.StringVar(&cfg.DefDir, "def-dir", "", "tui will make edits to strategies in this manual build definition repo")
	set.StringVar(&cfg.LLMProject, "llm-project", "", "if provided, the GCP project to prefer over --project for use with the Vertext AI API")
	set.StringVar(&cfg.RundexGCSPath, "rundex-gcs-path", "", "if provided, use a GCS path as the rundex")
	set.StringVar(&cfg.SharedAssetStore, "merged-asset-store", "", "if provided, use a GCS path as a shared asset store")
	set.StringVar(&cfg.BootstrapBucket, "bootstrap-bucket", "", "the gcs bucket where bootstrap tools are stored")
	set.StringVar(&cfg.BootstrapVersion, "bootstrap-version", "", "the version of bootstrap tools to use")
	return set
}
