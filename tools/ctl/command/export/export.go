// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package export

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/cheggaaa/pb"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/difftool"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the export command.
type Config struct {
	Project        string
	Run            string
	Destination    string
	Pattern        string
	ExportRundex   bool
	AssetTypes     string
	MaxConcurrency int
	DebugStorage   string
	LogsBucket     string
	MetadataBucket string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Run == "" {
		return errors.New("run is required")
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

func buildFetchRebuildRequest(bench, run, prefix, pattern string, clean, latestPerPackage bool) (*rundex.FetchRebuildRequest, error) {
	var runs []string
	if run != "" {
		runs = strings.Split(run, ",")
	}
	req := rundex.FetchRebuildRequest{
		Runs: runs,
		Opts: rundex.FetchRebuildOpts{
			Prefix:  prefix,
			Pattern: pattern,
			Clean:   clean,
		},
		LatestPerPackage: latestPerPackage,
	}
	if len(req.Runs) == 0 {
		return nil, errors.New("'run' must be supplied")
	}
	// Load the benchmark, if provided.
	if bench != "" {
		log.Printf("Extracting benchmark %s...\n", filepath.Base(bench))
		set, err := benchmark.ReadBenchmark(bench)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		req.Bench = &set
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	return &req, nil
}

// Handler contains the business logic for the export command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	runID := cfg.Run
	var destDex rundex.Writer
	var destStore rebuild.LocatableAssetStore
	{
		destURL, err := url.Parse(cfg.Destination)
		if err != nil {
			return nil, errors.Wrap(err, "parsing destination")
		}
		switch destURL.Scheme {
		case "gs":
			client, err := gcs.NewClient(ctx)
			if err != nil {
				return nil, errors.Wrap(err, "creating gcs client")
			}
			// Add the layout.AssetsDir to the path
			destStoreURL := urlx.MustParse(destURL.String())
			destStoreURL.Path = path.Join(destStoreURL.Path, layout.AssetsDir)
			destStore, err = rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, runID), client, destStoreURL.String())
			if err != nil {
				return nil, errors.Wrap(err, "creating gcs asset store")
			}
			// rundex.NewGCSClient already adds layout.RundexDir to the path
			destDex = rundex.NewGCSClient(ctx, client, destURL.Host, destURL.Path)
		case "file":
			dir := filepath.Join(destURL.Path, layout.AssetsDir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, errors.Wrapf(err, "failed to create directory %s", dir)
			}
			assetsFS, err := osfs.New("/").Chroot(dir)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
			}
			destStore = rebuild.NewFilesystemAssetStoreWithRunID(assetsFS, runID)
			// TODO: Find a helper to re-use for these two dirs
			dir = filepath.Join(destURL.Path, layout.RundexDir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, errors.Wrapf(err, "failed to create directory %s", dir)
			}
			rundexFS, err := osfs.New("/").Chroot(dir)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
			}
			destDex = rundex.NewFilesystemClient(rundexFS)
		default:
			return nil, errors.New("destination must be a gs:// or file:// URL")
		}
	}
	var assetTypes []rebuild.AssetType
	if cfg.AssetTypes != "" {
		for _, at := range strings.Split(cfg.AssetTypes, ",") {
			assetTypes = append(assetTypes, rebuild.AssetType(at))
		}
	}
	var fireDex rundex.Reader
	var err error
	fireDex, err = rundex.NewFirestore(ctx, cfg.Project)
	if err != nil {
		return nil, err
	}
	mux := meta.NewRegistryMux(http.DefaultClient)
	butler := localfiles.NewButler(cfg.MetadataBucket, cfg.LogsBucket, cfg.DebugStorage, mux, func(_ string) (rebuild.LocatableAssetStore, error) { return destStore, nil })
	// Butler doesn't handle non-local asset stores well, so we need a local-based butler to implement diffoscope.
	localAssets, err := localfiles.AssetStore(runID)
	if err != nil {
		return nil, errors.Wrap(err, "making local asset store")
	}
	localButler := localfiles.NewButler(cfg.MetadataBucket, cfg.LogsBucket, cfg.DebugStorage, mux, func(_ string) (rebuild.LocatableAssetStore, error) { return localAssets, nil })
	// Write the metadata about the run.
	if cfg.ExportRundex {
		log.Println("Exporting run_metadata")
		runs, err := fireDex.FetchRuns(ctx, rundex.FetchRunsOpts{IDs: []string{runID}})
		if err != nil {
			log.Printf("fetching runs failed: %v", err)
		} else {
			if len(runs) != 1 {
				return nil, errors.Errorf("expected exactly one run, got %d", len(runs))
			}
			for _, run := range runs {
				if err := destDex.WriteRun(ctx, run); err != nil {
					return nil, errors.Wrapf(err, "writing run %s failed", run.ID)
				}
			}
		}
		log.Printf("Exported run_metadata for run: %s", runID)
	}
	// Read all the rebuild objects.
	var rebuilds []rundex.Rebuild
	{
		req, err := buildFetchRebuildRequest("", runID, "", cfg.Pattern, false, false)
		if err != nil {
			return nil, err
		}
		log.Printf("Querying results for [run=%v,pattern=%s]", req.Runs, req.Opts.Pattern)
		rebuilds, err = fireDex.FetchRebuilds(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	log.Printf("Fetched %d rebuilds", len(rebuilds))
	// Export all the run objects.
	rundexReadParallelism := cfg.MaxConcurrency
	type rebuildExport struct {
		rebuild rundex.Rebuild
		errs    []error
	}
	p := pipe.ParInto(rundexReadParallelism, pipe.FromSlice(rebuilds), func(in rundex.Rebuild, out chan<- rebuildExport) {
		res := rebuildExport{rebuild: in}
		defer func() { out <- res }()
		if cfg.ExportRundex {
			if err := destDex.WriteRebuild(ctx, in); err != nil {
				res.errs = append(res.errs, errors.Wrap(err, "writing rebuild to rundex"))
			}
		}
		for _, at := range assetTypes {
			if at == difftool.DiffoscopeAsset {
				a := at.For(in.Target())
				if _, err := localButler.Fetch(ctx, runID, a); err != nil {
					res.errs = append(res.errs, errors.Wrapf(err, "fetching diff for %s", at))
					continue
				}
				if err := rebuild.AssetCopy(ctx, destStore, localAssets, a); err != nil {
					res.errs = append(res.errs, errors.Wrapf(err, "copying diff for %s", at))
				}
				continue
			}
			if _, err := butler.Fetch(ctx, runID, at.For(in.Target())); err != nil {
				res.errs = append(res.errs, errors.Wrapf(err, "fetching %s", at))
			}
		}
	})
	log.Println("Beginning export of rebuilds...")
	bar := pb.New(len(rebuilds))
	bar.Output = deps.IO.Err
	bar.ShowTimeLeft = true
	bar.Start()
	for re := range p.Out() {
		if len(re.errs) > 0 {
			for _, err := range re.errs {
				fmt.Fprintf(deps.IO.Out, "%s: %v\n", re.rebuild.ID(), err)
			}
		}
		bar.Increment()
	}
	bar.Finish()
	return &act.NoOutput{}, nil
}

// Command creates a new export command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "export -project <ID> -run <ID> -destination <url> [-pattern <regex>] [-rundex] [-asset-types <type1>,<type2>] [--max-concurrency N]",
		Short: "Export rebuild results",
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
	set.StringVar(&cfg.Run, "run", "", "the run from which to export results")
	set.StringVar(&cfg.Destination, "destination", "", "the destination for the export, e.g. gs://bucket/prefix")
	set.StringVar(&cfg.Pattern, "pattern", "", "filter results to those matching this regex pattern")
	set.BoolVar(&cfg.ExportRundex, "rundex", false, "whether to include the rundex in the export")
	set.StringVar(&cfg.AssetTypes, "asset-types", "", "a comma-separated list of asset types to export")
	set.IntVar(&cfg.MaxConcurrency, "max-concurrency", 90, "maximum number of inflight requests")
	set.StringVar(&cfg.DebugStorage, "debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	set.StringVar(&cfg.LogsBucket, "logs-bucket", "", "the gcs bucket where gcb logs are stored")
	set.StringVar(&cfg.MetadataBucket, "metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	return set
}
