// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	gcs "cloud.google.com/go/storage"
	"github.com/cheggaaa/pb"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/benchmark/run"
	"github.com/google/oss-rebuild/tools/ctl/gradle"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/migrations"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/serviceusage/v1"
	"google.golang.org/genai"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

const vertexAIService = "aiplatform.googleapis.com"

var rootCmd = &cobra.Command{
	Use:   "ctl",
	Short: "A debugging tool for OSS-Rebuild",
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

var tui = &cobra.Command{
	Use:   "tui [--project <ID>] [--debug-storage <bucket>] [--benchmark-dir <dir>] [--clean] [--llm-project] [--rundex-gcs-path <path>] [--merged-asset-store <path>]",
	Short: "A terminal UI for the OSS-Rebuild debugging tools",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if *debugStorage != "" {
			u, err := url.Parse(*debugStorage)
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing --debug-storage as url"))
			}
			if u.Scheme == "gs" {
				prefix := strings.TrimPrefix(u.Path, "/")
				if prefix != "" {
					log.Fatalf("--debug-storage cannot have additional path elements, found %s", prefix)
				}
			}
		}
		var dex rundex.Reader
		var watcher rundex.Watcher
		{
			if *rundexGCSPath != "" {
				u, err := url.Parse(*rundexGCSPath)
				if err != nil {
					log.Fatal(errors.Wrap(err, "parsing --rundex-gcs-path"))
				}
				if u.Scheme != "gs" {
					log.Fatal("--rundex-gcs-path must be a gs:// URL")
				}
				ctx := context.WithValue(cmd.Context(), rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
				gcsClient, err := gcs.NewClient(ctx)
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating GCS client"))
				}
				dex = rundex.NewGCSClient(ctx, gcsClient, u.Host, strings.TrimPrefix(u.Path, "/"))
				// GCS watcher is not implemented.
			} else if *project != "" {
				var err error
				dex, err = rundex.NewFirestore(cmd.Context(), *project)
				if err != nil {
					log.Fatal(err)
				}
			} else {
				lc := rundex.NewLocalClient(localfiles.Rundex())
				dex = lc
				watcher = lc
			}
		}
		var buildDefs *rebuild.FilesystemAssetStore
		if *defDir != "" {
			if fs, err := osfs.New("/").Chroot(*defDir); err != nil {
				log.Fatal(errors.Wrap(err, "creating asset store in build def dir"))
			} else {
				buildDefs = rebuild.NewFilesystemAssetStore(fs)
			}
		} else {
			var err error
			if buildDefs, err = localfiles.BuildDefs(); err != nil {
				log.Fatal(errors.Wrap(err, "failed to create local build def asset store"))
			}
		}
		regclient := http.DefaultClient
		mux := rebuild.RegistryMux{
			Debian:   debianreg.HTTPRegistry{Client: regclient},
			CratesIO: cratesreg.HTTPRegistry{Client: regclient},
			NPM:      npmreg.HTTPRegistry{Client: regclient},
			PyPI:     pypireg.HTTPRegistry{Client: regclient},
			Maven:    mavenreg.HTTPRegistry{Client: regclient},
		}
		var assetStoreFn func(runID string) (rebuild.LocatableAssetStore, error)
		if *sharedAssetStore != "" {
			u, err := url.Parse(*sharedAssetStore)
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing --merged-asset-store"))
			}
			// TODO: Add support for local filesystem based merged-asset-store
			if u.Scheme != "gs" {
				log.Fatal("--merged-asset-store currently only supports gs:// URLs")
			}
			assetStoreFn = func(runID string) (rebuild.LocatableAssetStore, error) {
				frontline, err := localfiles.AssetStore(runID)
				if err != nil {
					return nil, err
				}
				ctx := context.WithValue(cmd.Context(), rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
				ctx = context.WithValue(ctx, rebuild.RunID, runID)
				backline, err := rebuild.NewGCSStore(ctx, *sharedAssetStore)
				if err != nil {
					return nil, err
				}
				return rebuild.NewCachedAssetStore(frontline, backline), nil
			}
		} else {
			assetStoreFn = localfiles.AssetStore
		}
		butler := localfiles.NewButler(*metadataBucket, *logsBucket, *debugStorage, mux, assetStoreFn)
		var aiClient *genai.Client
		{
			aiProject := *project
			if *llmProject != "" {
				aiProject = *llmProject
			}
			if aiProject != "" {
				serviceUsageClient, err := serviceusage.NewService(cmd.Context(), option.WithScopes(serviceusage.CloudPlatformScope))
				if err != nil {
					log.Fatalf("Failed to create Service Usage client: %v", err)
				}
				if service, err := serviceUsageClient.Services.Get(fmt.Sprintf("projects/%s/services/%s", aiProject, vertexAIService)).Do(); err != nil {
					log.Fatalf("Failed to check for vertex AI service: %v", err)
				} else if service.State == "ENABLED" {
					aiClient, err = genai.NewClient(cmd.Context(), &genai.ClientConfig{
						Backend:  genai.BackendVertexAI,
						Project:  aiProject,
						Location: "us-central1",
					})
					if err != nil {
						log.Fatal(errors.Wrap(err, "failed to create a genai client"))
					}
				}
			}
		}
		benches := benchmark.NewFSRepository(osfs.New(*benchmarkDir))
		tapp := ide.NewTuiApp(dex, watcher, rundex.FetchRebuildOpts{Clean: *clean}, benches, buildDefs, butler, aiClient)
		if err := tapp.Run(cmd.Context()); err != nil {
			// TODO: This cleanup will be unnecessary once NewTuiApp does split logging.
			log.Default().SetOutput(os.Stdout)
			log.Fatal(err)
		}
	},
}

var getResults = &cobra.Command{
	Use:   "get-results -project <ID> -run <ID> [-bench <benchmark.json>] [-prefix <prefix>] [-pattern <regex>] [-sample N] [-format=summary|bench|csv]",
	Short: "Analyze rebuild results",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		req, err := buildFetchRebuildRequest(*bench, *runFlag, *prefix, *pattern, *clean, true)
		if err != nil {
			log.Fatal(err)
		}
		fireClient, err := rundex.NewFirestore(cmd.Context(), *project)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Querying results for [executors=%v,runs=%v,bench=%s,prefix=%s,pattern=%s]", req.Executors, req.Runs, *bench, req.Opts.Prefix, req.Opts.Pattern)
		rebuilds, err := fireClient.FetchRebuilds(cmd.Context(), req)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Fetched %d rebuilds", len(rebuilds))
		byCount := rundex.GroupRebuilds(rebuilds)
		if len(byCount) == 0 {
			log.Println("No results")
			return
		}
		switch *format {
		case "", "summary":
			log.Println("Verdict summary:")
			for _, vg := range byCount {
				fmt.Printf(" %4d - %s (example: %s)\n", vg.Count, vg.Msg[:min(len(vg.Msg), 1000)], vg.Examples[0].ID())
			}
			successes := 0
			for _, r := range rebuilds {
				if r.Success {
					successes++
				}
			}
			fmt.Printf("%d succeeded of %d  (%2.1f%%)\n", successes, len(rebuilds), 100.*float64(successes)/float64(len(rebuilds)))
		case "bench":
			var ps benchmark.PackageSet
			if *sample > 0 && *sample < len(rebuilds) {
				ps.Count = *sample
			} else {
				ps.Count = len(rebuilds)
			}
			rng := rand.New(rand.NewSource(int64(ps.Count)))
			var rbs []rundex.Rebuild
			for _, r := range rebuilds {
				rbs = append(rbs, r)
			}
			slices.SortFunc(rbs, func(a rundex.Rebuild, b rundex.Rebuild) int { return strings.Compare(a.ID(), b.ID()) })
			rng.Shuffle(len(rbs), func(i int, j int) {
				rbs[i], rbs[j] = rbs[j], rbs[i]
			})
			for _, r := range rbs[:ps.Count] {
				idx := -1
				for i, psp := range ps.Packages {
					if psp.Name == r.Package {
						idx = i
						break
					}
				}
				if idx == -1 {
					ps.Packages = append(ps.Packages, benchmark.Package{Name: r.Package, Ecosystem: r.Ecosystem})
					idx = len(ps.Packages) - 1
				}
				ps.Packages[idx].Versions = append(ps.Packages[idx].Versions, r.Version)
				if r.Artifact != "" {
					ps.Packages[idx].Artifacts = append(ps.Packages[idx].Artifacts, r.Artifact)
				}
			}
			ps.Updated = time.Now()
			b, err := json.MarshalIndent(ps, "", "  ")
			if err != nil {
				log.Fatal(errors.Wrap(err, "marshalling benchmark"))
			}
			fmt.Println(string(b))
		case "csv":
			w := csv.NewWriter(cmd.OutOrStdout())
			defer w.Flush()
			for _, r := range rebuilds {
				if err := w.Write([]string{r.Ecosystem, r.Package, r.Version, r.Artifact, r.RunID, r.Message}); err != nil {
					log.Fatal(errors.Wrap(err, "writing CSV"))
				}
			}
		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
	},
}

var export = &cobra.Command{
	Use:   "export -project <ID> -run <ID> -destination <url> [-pattern <regex>] [-rundex] [-asset-types <type1>,<type2>] -",
	Short: "Export rebuild results",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if *runFlag == "" {
			log.Fatal("--run must be provided")
		}
		runID := *runFlag
		ctx := cmd.Context()
		var destDex rundex.Writer
		var destStore rebuild.LocatableAssetStore
		{
			destURL, err := url.Parse(*destination)
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing destination"))
			}
			switch destURL.Scheme {
			case "gs":
				client, err := gcs.NewClient(ctx)
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating gcs client"))
				}
				// Add the layout.AssetsDir to the path
				destStoreURL := urlx.MustParse(destURL.String())
				destStoreURL.Path = path.Join(destStoreURL.Path, layout.AssetsDir)
				destStore, err = rebuild.NewGCSStoreFromClient(context.WithValue(ctx, rebuild.RunID, runID), client, destStoreURL.String())
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating gcs asset store"))
				}
				// rundex.NewGCSClient already adds layout.RundexDir to the path
				destDex = rundex.NewGCSClient(ctx, client, destURL.Host, destURL.Path)
			case "file":
				dir := filepath.Join(destURL.Path, layout.AssetsDir)
				if err := os.MkdirAll(dir, 0755); err != nil {
					log.Fatal(errors.Wrapf(err, "failed to create directory %s", dir))
				}
				assetsFS, err := osfs.New("/").Chroot(dir)
				if err != nil {
					log.Fatal(errors.Wrapf(err, "failed to chroot into directory %s", dir))
				}
				destStore = rebuild.NewFilesystemAssetStoreWithRunID(assetsFS, runID)
				// TODO: Find a helper to re-use for these two dirs
				dir = filepath.Join(destURL.Path, layout.RundexDir)
				if err := os.MkdirAll(dir, 0755); err != nil {
					log.Fatal(errors.Wrapf(err, "failed to create directory %s", dir))
				}
				rundexFS, err := osfs.New("/").Chroot(dir)
				if err != nil {
					log.Fatal(errors.Wrapf(err, "failed to chroot into directory %s", dir))
				}
				destDex = rundex.NewLocalClient(rundexFS)
			default:
				log.Fatal("destination must be a gs:// or file:// URL")
			}
		}
		var assetTypes []rebuild.AssetType
		if *assetTypesFlag != "" {
			for _, at := range strings.Split(*assetTypesFlag, ",") {
				assetTypes = append(assetTypes, rebuild.AssetType(at))
			}
		}
		var fireDex rundex.Reader
		var err error
		fireDex, err = rundex.NewFirestore(ctx, *project)
		if err != nil {
			log.Fatal(err)
		}
		regclient := http.DefaultClient
		mux := rebuild.RegistryMux{
			Debian:   debianreg.HTTPRegistry{Client: regclient},
			CratesIO: cratesreg.HTTPRegistry{Client: regclient},
			NPM:      npmreg.HTTPRegistry{Client: regclient},
			PyPI:     pypireg.HTTPRegistry{Client: regclient},
		}
		butler := localfiles.NewButler(*metadataBucket, *logsBucket, *debugStorage, mux, func(_ string) (rebuild.LocatableAssetStore, error) { return destStore, nil })
		// Write the metadata about the run.
		if *exportRundex {
			log.Println("Exporting run_metadata")
			runs, err := fireDex.FetchRuns(ctx, rundex.FetchRunsOpts{IDs: []string{runID}})
			if err != nil {
				log.Printf("fetching runs failed: %v", err)
			} else {
				if len(runs) != 1 {
					log.Fatalf("expected exactly one run, got %d", len(runs))
				}
				for _, run := range runs {
					if err := destDex.WriteRun(ctx, run); err != nil {
						log.Fatalf("writing run %s failed: %v", run.ID, err)
					}
				}
			}
			log.Printf("Exported run_metadata for run: %s", runID)
		}
		// Read all the rebuild objects.
		var rebuilds []rundex.Rebuild
		{
			req, err := buildFetchRebuildRequest("", runID, "", *pattern, false, false)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Querying results for [run=%v,pattern=%s]", req.Runs, req.Opts.Pattern)
			rebuilds, err = fireDex.FetchRebuilds(ctx, req)
			if err != nil {
				log.Fatal(err)
			}
		}
		log.Printf("Fetched %d rebuilds", len(rebuilds))
		// Export all the run objects.
		rundexReadParallelism := 50
		type rebuildExport struct {
			rebuild rundex.Rebuild
			errs    []error
		}
		p := pipe.ParInto(rundexReadParallelism, pipe.FromSlice(rebuilds), func(in rundex.Rebuild, out chan<- rebuildExport) {
			res := rebuildExport{rebuild: in}
			defer func() { out <- res }()
			if *exportRundex {
				if err := destDex.WriteRebuild(ctx, in); err != nil {
					res.errs = append(res.errs, errors.Wrap(err, "writing rebuild to rundex"))
				}
			}
			for _, at := range assetTypes {
				// NOTE: We hardcode wasSmoketest=false, because in.WasSmoketest() incorrectly returns true if the attempt failed during rebuild, resulting in an empty ObliviousID
				if _, err := butler.Fetch(ctx, runID, false, at.For(in.Target())); err != nil {
					res.errs = append(res.errs, errors.Wrapf(err, "fetching %s", at))
				}
			}
		})
		log.Println("Beginning export of rebuilds...")
		errOut := cmd.OutOrStdout()
		bar := pb.New(len(rebuilds))
		bar.Output = cmd.OutOrStderr()
		bar.ShowTimeLeft = true
		bar.Start()
		for re := range p.Out() {
			if len(re.errs) > 0 {
				for _, err := range re.errs {
					fmt.Fprintf(errOut, "%s: %v\n", re.rebuild.ID(), err)
				}
			}
			bar.Increment()
		}
		bar.Finish()
	},
}

func isCloudRun(u *url.URL) bool {
	return strings.HasSuffix(u.Host, ".run.app")
}

var runBenchmark = &cobra.Command{
	Use:   "run-bench smoketest|attest -api <URI>  [-local -prebuild-bucket <BUCKET> -prebuild-version <VERSION>] [-format=summary|csv] <benchmark.json>",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		mode := schema.ExecutionMode(args[0])
		if mode != schema.SmoketestMode && mode != schema.AttestMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
		}
		var apiURL *url.URL
		var set benchmark.PackageSet
		var err error
		{
			path := args[1]
			log.Printf("Extracting benchmark %s...\n", filepath.Base(path))
			set, err = benchmark.ReadBenchmark(path)
			if err != nil {
				log.Fatal(errors.Wrap(err, "reading benchmark file"))
			}
			log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
		}
		var runID string
		var dex rundex.Writer
		var executor run.ExecutionService
		concurrency := *maxConcurrency
		if *buildLocal {
			now := time.Now().UTC()
			runID = now.Format(time.RFC3339)
			if concurrency != 1 {
				log.Println("Note: dropping max concurrency to 1 due to local execution")
			}
			concurrency = 1
			store, err := localfiles.AssetStore(runID)
			if err != nil {
				log.Fatalf("Failed to create temp directory: %v", err)
			}
			// TODO: Validate this.
			prebuildURL := fmt.Sprintf("https://%s.storage.googleapis.com/%s", *prebuildBucket, *prebuildVersion)
			executor = run.NewLocalExecutionService(prebuildURL, store, cmd.OutOrStdout())
			dex = rundex.NewLocalClient(localfiles.Rundex())
			if err := dex.WriteRun(ctx, rundex.FromRun(schema.Run{
				ID:            runID,
				BenchmarkName: filepath.Base(args[1]),
				BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
				Type:          string(schema.SmoketestMode),
				Created:       now,
			})); err != nil {
				log.Println(errors.Wrap(err, "writing run to rundex"))
			}
		} else {
			if *apiUri == "" {
				log.Fatal("API endpoint not provided")
			}
			apiURL, err := url.Parse(*apiUri)
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing API endpoint"))
			}
			var client *http.Client
			if isCloudRun(apiURL) {
				// If the api is on Cloud Run, we need to use an authorized client.
				apiURL.Scheme = "https"
				client, err = oauth.AuthorizedUserIDClient(ctx)
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating authorized HTTP client"))
				}
			} else {
				client = http.DefaultClient
			}
			executor = run.NewRemoteExecutionService(client, apiURL)
			stub := api.Stub[schema.CreateRunRequest, schema.Run](client, apiURL.JoinPath("runs"))
			resp, err := stub(ctx, schema.CreateRunRequest{
				BenchmarkName: filepath.Base(args[1]),
				BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
				Type:          string(mode),
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating run"))
			}
			runID = resp.ID
		}
		if *async {
			if *buildLocal {
				log.Fatal("Unsupported async local execution")
			}
			queue, err := taskqueue.NewQueue(ctx, *taskQueuePath, *taskQueueEmail)
			if err != nil {
				log.Fatal(errors.Wrap(err, "making taskqueue client"))
			}
			if err := run.RunBenchAsync(ctx, set, mode, apiURL, runID, queue); err != nil {
				log.Fatal(errors.Wrap(err, "adding benchmark to queue"))
			}
			return
		}
		bar := pb.New(set.Count)
		bar.Output = cmd.OutOrStderr()
		bar.ShowTimeLeft = true
		verdictChan, err := run.RunBench(ctx, set, run.RunBenchOpts{
			ExecService:       executor,
			Mode:              mode,
			RunID:             runID,
			MaxConcurrency:    concurrency,
			UseSyscallMonitor: *useSyscallMonitor,
			UseNetworkProxy:   *useNetworkProxy,
		})
		if err != nil {
			log.Fatal(errors.Wrap(err, "running benchmark"))
		}
		var verdicts []schema.Verdict
		bar.Start()
		for v := range verdictChan {
			bar.Increment()
			if *verbose && v.Message != "" {
				fmt.Printf("\n%v: %s\n", v.Target, v.Message)
			}
			if dex != nil {
				if err := dex.WriteRebuild(ctx, rundex.NewRebuildFromVerdict(v, "local", runID, time.Now().UTC())); err != nil {
					log.Println(errors.Wrap(err, "writing rebuild to rundex"))
				}
			}
			verdicts = append(verdicts, v)
		}
		bar.Finish()
		sort.Slice(verdicts, func(i, j int) bool {
			return fmt.Sprint(verdicts[i].Target) > fmt.Sprint(verdicts[j].Target)
		})
		switch *format {
		// TODO: Maybe add more format options, or include more data in the csv?
		case "", "summary":
			var successes int
			for _, v := range verdicts {
				if v.Message == "" {
					successes++
				}
			}
			io.WriteString(cmd.OutOrStdout(), fmt.Sprintf("Successes: %d/%d\n", successes, len(verdicts)))
		case "csv":
			w := csv.NewWriter(cmd.OutOrStdout())
			defer w.Flush()
			for _, v := range verdicts {
				if err := w.Write([]string{fmt.Sprintf("%v", v.Target), v.Message}); err != nil {
					log.Fatal(errors.Wrap(err, "writing CSV"))
				}
			}
		default:
			log.Fatalf("Unsupported format: %s", *format)
		}
	},
}

const analyzeMode = schema.ExecutionMode("analyze")

var runOne = &cobra.Command{
	Use:   "run-one smoketest|attest|analyze --api <URI> --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--strategy <strategy.yaml>] [--strategy-from-repo]",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if *ecosystem == "" || *pkg == "" || *version == "" {
			log.Fatal("ecosystem, package, and version must be provided")
		}
		mode := schema.ExecutionMode(args[0])
		if mode != schema.SmoketestMode && mode != schema.AttestMode && mode != analyzeMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest', 'attest', or 'analyze'", string(mode))
		}
		if *apiUri == "" {
			log.Fatal("API endpoint not provided")
		}
		apiURL, err := url.Parse(*apiUri)
		if err != nil {
			log.Fatal(errors.Wrap(err, "parsing API endpoint"))
		}
		ctx := cmd.Context()
		var client *http.Client
		{
			if strings.Contains(apiURL.Host, "run.app") {
				// If the api is on Cloud Run, we need to use an authorized client.
				apiURL.Scheme = "https"
				client, err = oauth.AuthorizedUserIDClient(ctx)
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating authorized HTTP client"))
				}
			} else {
				client = http.DefaultClient
			}
		}
		var strategy *schema.StrategyOneOf
		{
			if *strategyPath != "" {
				if mode == schema.AttestMode {
					log.Fatal("--strategy not supported in attest mode, use --strategy-from-repo")
				}
				if mode == analyzeMode {
					log.Fatal("--strategy not supported in analyze mode")
				}
				f, err := os.Open(*strategyPath)
				if err != nil {
					return
				}
				defer f.Close()
				strategy = &schema.StrategyOneOf{}
				err = yaml.NewDecoder(f).Decode(strategy)
				if err != nil {
					log.Fatal(errors.Wrap(err, "reading strategy file"))
				}
			}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		switch mode {
		case analyzeMode:
			stub := api.Stub[schema.AnalyzeRebuildRequest, api.NoReturn](client, apiURL.JoinPath("analyze"))
			_, err := stub(ctx, schema.AnalyzeRebuildRequest{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Version:   *version,
				Artifact:  *artifact,
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "running analyze"))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Analysis completed successfully")
		case schema.SmoketestMode:
			stub := api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](client, apiURL.JoinPath("smoketest"))
			resp, err := stub(ctx, schema.SmoketestRequest{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Versions:  []string{*version},
				Strategy:  strategy,
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "running smoketest"))
			}
			for _, v := range resp.Verdicts {
				if err := enc.Encode(v); err != nil {
					log.Fatal(errors.Wrap(err, "encoding results"))
				}
			}
		case schema.AttestMode:
			stub := api.Stub[schema.RebuildPackageRequest, schema.Verdict](client, apiURL.JoinPath("rebuild"))
			resp, err := stub(ctx, schema.RebuildPackageRequest{
				Ecosystem:         rebuild.Ecosystem(*ecosystem),
				Package:           *pkg,
				Version:           *version,
				Artifact:          *artifact,
				UseNetworkProxy:   *useNetworkProxy,
				UseSyscallMonitor: *useSyscallMonitor,
				ID:                time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "running attest"))
			}
			if err := enc.Encode(resp); err != nil {
				log.Fatal(errors.Wrap(err, "encoding result"))
			}
		}
	},
}

var listRuns = &cobra.Command{
	Use:   "list-runs -project <ID> [ -bench <benchmark.json> ]",
	Short: "List runs",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		var opts rundex.FetchRunsOpts
		if *bench != "" {
			log.Printf("Extracting benchmark %s...\n", filepath.Base(*bench))
			set, err := benchmark.ReadBenchmark(*bench)
			if err != nil {
				log.Fatal(errors.Wrap(err, "reading benchmark file"))
			}
			log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
			opts.BenchmarkHash = hex.EncodeToString(set.Hash(sha256.New()))
		}
		if *project == "" {
			log.Fatal("project not provided")
		}
		client, err := rundex.NewFirestore(ctx, *project)
		if err != nil {
			log.Fatal(errors.Wrap(err, "creating firestore client"))
		}
		runs, err := client.FetchRuns(ctx, opts)
		if err != nil {
			log.Fatal("GetRuns error", err.Error())
		}
		var count int
		for _, r := range runs {
			fmt.Printf("  %s [bench=%s hash=%s]\n", r.ID, r.BenchmarkName, r.BenchmarkHash)
			count++
		}
		switch count {
		case 0:
			fmt.Println("No results found")
		case 1:
			fmt.Println("1 result found")
		default:
			fmt.Printf("%d results found\n", count)
		}
	},
}

var migrate = &cobra.Command{
	Use:   "migrate --project <project> [--dryrun] <migration-name>",
	Short: "Migrate firestore entries",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if *project == "" {
			log.Fatal("project not provided")
		}
		client, err := firestore.NewClient(cmd.Context(), *project)
		if err != nil {
			log.Fatal(errors.Wrap(err, "creating firestore client"))
		}
		migration, ok := migrations.All[args[0]]
		if !ok {
			log.Fatalf("Unknown migration: %s", args[0])
		}
		q := client.CollectionGroup(migration.CollectionGroup).Query
		bw := client.BulkWriter(cmd.Context())
		var total, updated int
		{
			ag := q.NewAggregationQuery()
			ag = ag.WithCount("total-count")
			res, err := ag.Get(cmd.Context())
			if err != nil {
				log.Fatal(errors.Wrap(err, "getting count"))
			}
			totalV, ok := res["total-count"].(*firestorepb.Value)
			if !ok {
				log.Fatalf("Couldn't get total count: %+v", res)
			}
			total = int(totalV.GetIntegerValue())
		}
		iter := q.Documents(cmd.Context())
		bar := pb.New(total)
		bar.Output = cmd.OutOrStderr()
		bar.ShowTimeLeft = true
		bar.Start()
		defer bar.Finish()
		for {
			doc, err := iter.Next()
			if err == iterator.Done {
				break
			}
			bar.Increment()
			if err != nil {
				log.Fatal(errors.Wrap(err, "iterating over attempts"))
			}
			updates, err := migration.Transform(doc)
			if errors.Is(err, migrations.ErrSkip) {
				continue
			} else if err != nil {
				log.Fatal(errors.Wrap(err, "transforming field"))
			}
			updated++
			if !*dryrun {
				if _, err := bw.Update(doc.Ref, updates); err != nil {
					log.Fatal(errors.Wrap(err, "updating field"))
				}
			}
		}
		bar.Finish()
		bw.End()
		log.Printf("Updated %d/%d entries (%2.1f%%)", updated, total, 100.*float64(updated)/float64(total))
	},
}

var infer = &cobra.Command{
	Use:   "infer --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--api <URI>] [--format strategy|dockerfile|debug-steps]",
	Short: "Run inference",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		req := schema.InferenceRequest{
			Ecosystem: rebuild.Ecosystem(*ecosystem),
			Package:   *pkg,
			Version:   *version,
			Artifact:  *artifact,
			// TODO: Add support for strategy hint.
		}
		var resp *schema.StrategyOneOf
		if *apiUri != "" {
			apiURL, err := url.Parse(*apiUri)
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing API endpoint"))
			}
			var client *http.Client
			if isCloudRun(apiURL) {
				// If the api is on Cloud Run, we need to use an authorized client.
				apiURL.Scheme = "https"
				client, err = oauth.AuthorizedUserIDClient(cmd.Context())
				if err != nil {
					log.Fatal(errors.Wrap(err, "creating authorized HTTP client"))
				}
			} else {
				client = http.DefaultClient
			}
			stub := api.Stub[schema.InferenceRequest, schema.StrategyOneOf](client, apiURL.JoinPath("/infer"))
			resp, err = stub(cmd.Context(), req)
			if err != nil {
				log.Fatal(errors.Wrap(err, "executing inference"))
			}
		} else {
			deps := &inferenceservice.InferDeps{
				HTTPClient: http.DefaultClient,
				GitCache:   nil,
			}
			var err error
			resp, err = inferenceservice.Infer(cmd.Context(), req, deps)
			if err != nil {
				log.Fatal(err)
			}
		}
		s, err := resp.Strategy()
		if err != nil {
			log.Fatal(errors.Wrap(err, "parsing strategy"))
		}
		if s == nil {
			log.Fatal("no strategy")
		}
		inp := rebuild.Input{Target: rebuild.Target{
			Ecosystem: rebuild.Ecosystem(*ecosystem),
			Package:   *pkg,
			Version:   *version,
			Artifact:  *artifact,
		}, Strategy: s}
		resources := build.Resources{
			ToolURLs: map[build.ToolType]string{
				// Ex: https://storage.googleapis.com/google-rebuild-bootstrap-tools/v0.0.0-20250428204534-b35098b3c7b7/timewarp
				build.TimewarpTool: fmt.Sprintf("https://storage.googleapis.com/%s/%s/timewarp", *bootstrapBucket, *bootstrapVersion),
			},
			BaseImageConfig: build.DefaultBaseImageConfig(),
		}
		var dockerfile string
		{
			plan, err := local.NewDockerBuildPlanner().GeneratePlan(cmd.Context(), inp, build.PlanOptions{
				UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
				Resources:   resources,
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "generating plan"))
			}
			dockerfile = plan.Dockerfile
		}
		var buildScript string
		{
			plan, err := local.NewDockerRunPlanner().GeneratePlan(cmd.Context(), inp, build.PlanOptions{
				UseTimewarp: meta.AllRebuilders[inp.Target.Ecosystem].UsesTimewarp(inp),
				Resources:   resources,
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "generating plan"))
			}
			buildScript = plan.Script
		}
		switch *format {
		case "", "strategy":
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(resp); err != nil {
				log.Fatal(errors.Wrap(err, "encoding result"))
			}
		case "dockerfile":
			cmd.OutOrStdout().Write([]byte(dockerfile))
		case "debug-steps":
			buildScript := fmt.Sprintf(textwrap.Dedent(`
				#!/usr/bin/env bash
				set -eux
				cat <<'EOS' | docker buildx build --tag=img -
				%s
				EOS
				docker run --name=container img
				`[1:]), dockerfile)
			b := cloudbuild.Build{
				Steps: []*cloudbuild.BuildStep{
					{
						Name:   "gcr.io/cloud-builders/docker",
						Script: buildScript,
					},
				},
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(b); err != nil {
				log.Fatal(errors.Wrap(err, "encoding build steps"))
			}
		case "shell-script":
			cmd.OutOrStdout().Write([]byte(buildScript))
		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
	},
}

var getGradleGAV = &cobra.Command{
	Use:   "get-gradle-gav --repository <URI> --ref <ref>",
	Short: "Extracts GAV coordinates from a Gradle project at a given commit",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoURL := *repository
		sha := *ref

		tempDir, err := os.MkdirTemp("", "gradle-gav-")
		if err != nil {
			return errors.Wrap(err, "failed to create temp directory")
		}
		defer os.RemoveAll(tempDir)

		repo, err := git.PlainClone(tempDir, false, &git.CloneOptions{URL: repoURL})
		if err != nil {
			return errors.Wrap(err, "failed to clone repository")
		}
		wt, err := repo.Worktree()
		if err != nil {
			return errors.Wrap(err, "failed to get worktree")
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(sha)}); err != nil {
			return errors.Wrap(err, "failed to checkout commit")
		}

		gradleProject, err := gradle.RunPrintCoordinates(cmd.Context(), *repo, local.NewRealCommandExecutor())
		if err != nil {
			return errors.Wrap(err, "running printCoordinates")
		}

		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(gradleProject)
	},
}

func logFailure(f func() error) {
	if err := f(); err != nil {
		log.Println(err)
	}
}

var setTrackedPackagesCmd = &cobra.Command{
	Use:   "set-tracked --bench <benchmark.json> <gcs-bucket>",
	Short: "Set the list of tracked packages",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if *bench == "" {
			return errors.New("bench must be provided")
		}
		bucket := args[len(args)-1]
		set, err := benchmark.ReadBenchmark(*bench)
		if err != nil {
			return errors.Wrap(err, "reading benchmark file")
		}
		packageMap := make(feed.TrackedPackageIndex)
		for _, p := range set.Packages {
			eco := rebuild.Ecosystem(p.Ecosystem)
			if _, ok := packageMap[eco]; !ok {
				packageMap[eco] = make(map[string]bool)
			}
			packageMap[eco][p.Name] = true
		}
		data := make(feed.TrackedPackageSet)
		for ecoStr, packages := range packageMap {
			ecosystem := rebuild.Ecosystem(ecoStr)
			data[ecosystem] = make([]string, 0, len(packages))
			for pkg := range packages {
				data[ecosystem] = append(data[ecosystem], pkg)
			}
		}
		gcsClient, err := gcs.NewClient(ctx)
		if err != nil {
			return errors.Wrap(err, "creating gcs client")
		}
		obj := gcsClient.Bucket(bucket).Object(feed.TrackedPackagesFile)
		w := obj.NewWriter(ctx)
		defer logFailure(w.Close)
		gzw := gzip.NewWriter(w)
		defer logFailure(gzw.Close)
		if err := json.NewEncoder(gzw).Encode(data); err != nil {
			log.Fatal(errors.Wrap(err, "compressing and uploading tracked packages"))
		}
		return nil
	},
}

var getTrackedPackagesCmd = &cobra.Command{
	Use:   "get-tracked [--format=index|bench] <gcs-bucket> <generation-num>",
	Short: "Get the list of tracked packages",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		bucket := args[len(args)-2]
		gcsClient, err := gcs.NewClient(ctx)
		if err != nil {
			return errors.Wrap(err, "creating gcs client")
		}
		obj := gcsClient.Bucket(bucket).Object(feed.TrackedPackagesFile)
		gen, err := strconv.ParseInt(args[len(args)-1], 10, 64)
		if err != nil {
			return errors.Wrap(err, "parsing generation number")
		}
		idx, err := feed.ReadTrackedIndex(ctx, feed.NewGCSObjectDataSource(obj), gen)
		if err != nil {
			return err
		}
		switch *format {
		case "", "index":
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(idx); err != nil {
				log.Fatal(errors.Wrap(err, "encoding tracked package index"))
			}
			return nil
		case "bench":
			var b benchmark.PackageSet
			for eco, packages := range idx {
				for pkg := range packages {
					b.Packages = append(b.Packages, benchmark.Package{
						Name:      pkg,
						Ecosystem: string(eco),
					})
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(b); err != nil {
				log.Fatal(errors.Wrap(err, "encoding tracked package benchmark"))
			}
			return nil
		default:
			return errors.Errorf("Unknown --format type: %s", *format)
		}
	},
}

var runAgent = &cobra.Command{
	Use:   "run-agent --project <project> --api <URI> --ecosystem <ecosystem> --package <name> --version <version> --artifact <name>",
	Short: "Run benchmark",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if *project == "" {
			return errors.New("project must be provided")
		}
		fire, err := firestore.NewClient(cmd.Context(), *project)
		if err != nil {
			return errors.Wrap(err, "creating firestore client")
		}
		if *apiUri == "" {
			log.Fatal("API endpoint not provided")
		}
		apiURL, err := url.Parse(*apiUri)
		if err != nil {
			return errors.Wrap(err, "parsing API endpoint")
		}
		if *ecosystem == "" || *pkg == "" || *version == "" || *artifact == "" {
			log.Fatal("ecosystem, package, version, and artifact must be provided")
		}
		t := rebuild.Target{Ecosystem: rebuild.Ecosystem(*ecosystem), Package: *pkg, Version: *version, Artifact: *artifact}
		ctx := cmd.Context()
		var client *http.Client
		{
			if strings.Contains(apiURL.Host, "run.app") {
				// If the api is on Cloud Run, we need to use an authorized client.
				apiURL.Scheme = "https"
				client, err = oauth.AuthorizedUserIDClient(ctx)
				if err != nil {
					return errors.Wrap(err, "creating authorized HTTP client")
				}
			} else {
				client = http.DefaultClient
			}
		}
		stub := api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent"))
		resp, err := stub(ctx, schema.AgentCreateRequest{
			Target: t,
		})
		if err != nil {
			return errors.Wrap(err, "running attest")
		}
		log.Printf("Successfully started session %s", resp.SessionID)
		sessionDoc := fire.Collection("agent_sessions").Doc(resp.SessionID)
		var session schema.AgentSession
		for {
			time.Sleep(5 * time.Second)
			if session.Status != schema.AgentSessionStatusCompleted {
				sessionSnap, err := sessionDoc.Get(ctx)
				if err != nil && status.Code(err) == codes.NotFound {
					continue
				} else if err != nil {
					log.Fatal(errors.Wrap(err, "getting session document"))
				}
				var newSession schema.AgentSession
				if err := sessionSnap.DataTo(&newSession); err != nil {
					log.Fatal("deserializing session data")
				}
				// As a proxy for iterations running, log any updates on the session.
				// TODO: Fetch iteration records as the come in and log those either instead of or in addition to this.
				if newSession.Updated != session.Updated {
					log.Printf("Session updated: %+v", newSession)
					session = newSession
				}
			}
			if session.Status == schema.AgentSessionStatusCompleted {
				log.Printf("Session %s completed", session.ID)
				break
			}
		}
		return nil
	},
}
var (
	// Shared
	apiUri            = flag.String("api", "", "OSS Rebuild API endpoint URI")
	ecosystem         = flag.String("ecosystem", "", "the ecosystem")
	pkg               = flag.String("package", "", "the package name")
	version           = flag.String("version", "", "the version of the package")
	artifact          = flag.String("artifact", "", "the artifact name")
	verbose           = flag.Bool("v", false, "verbose output")
	bench             = flag.String("bench", "", "a path to a benchmark file for filtering or execution")
	debugStorage      = flag.String("debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	logsBucket        = flag.String("logs-bucket", "", "the gcs bucket where gcb logs are stored")
	metadataBucket    = flag.String("metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	bootstrapBucket   = flag.String("bootstrap-bucket", "", "the gcs bucket where bootstrap tools are stored")
	bootstrapVersion  = flag.String("bootstrap-version", "", "the version of bootstrap tools to use")
	useNetworkProxy   = flag.Bool("use-network-proxy", false, "request the newtwork proxy")
	useSyscallMonitor = flag.Bool("use-syscall-monitor", false, "request syscall monitoring")
	assetTypesFlag    = flag.String("asset-types", "", "a comma-separated list of asset types to export")
	// run-bench
	maxConcurrency  = flag.Int("max-concurrency", 90, "maximum number of inflight requests")
	buildLocal      = flag.Bool("local", false, "true if this request is going direct to build-local (not through API first)")
	prebuildBucket  = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	prebuildVersion = flag.String("prebuild-version", "", "golang version identifier of the prebuild binary builds")
	async           = flag.Bool("async", false, "true if this benchmark should run asynchronously")
	taskQueuePath   = flag.String("task-queue", "", "the path identifier of the task queue to use")
	taskQueueEmail  = flag.String("task-queue-email", "", "the email address of the serivce account Cloud Tasks should authorize as")
	// run-one
	strategyPath = flag.String("strategy", "", "the strategy file to use")
	// get-results
	runFlag = flag.String("run", "", "the run(s) from which to fetch results")
	format  = flag.String("format", "", "format of the output, options are command specific")
	prefix  = flag.String("prefix", "", "filter results to those matching this prefix ")
	pattern = flag.String("pattern", "", "filter results to those matching this regex pattern")
	sample  = flag.Int("sample", -1, "if provided, only N results will be displayed")
	project = flag.String("project", "", "the project from which to fetch the Firestore data")
	clean   = flag.Bool("clean", false, "whether to apply normalization heuristics to group similar verdicts")
	// get-gradle-gav
	repository = flag.String("repository", "", "the repository URI")
	ref        = flag.String("ref", "", "the git reference (branch, tag, commit)")
	// TUI
	benchmarkDir     = flag.String("benchmark-dir", "", "a directory with benchmarks to work with")
	defDir           = flag.String("def-dir", "", "tui will make edits to strategies in this manual build definition repo")
	llmProject       = flag.String("llm-project", "", "if provided, the GCP project to prefer over --project for use with the Vertext AI API")
	rundexGCSPath    = flag.String("rundex-gcs-path", "", "if provided, use a GCS path as the rundex")
	sharedAssetStore = flag.String("merged-asset-store", "", "if provided, use a GCS path as a shared asset store")
	// Migrate
	dryrun = flag.Bool("dryrun", false, "true if this migration is a dryrun")
	// Export
	destination  = flag.String("destination", "", "the destination for the export, e.g. gs://bucket/prefix")
	exportRundex = flag.Bool("rundex", false, "whether to include the rundex in the export")
)

func init() {
	runBenchmark.Flags().AddGoFlag(flag.Lookup("api"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("max-concurrency"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("local"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("prebuild-bucket"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("prebuild-version"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("format"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("v"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("async"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("task-queue"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("task-queue-email"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("use-network-proxy"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("use-syscall-monitor"))

	runOne.Flags().AddGoFlag(flag.Lookup("api"))
	runOne.Flags().AddGoFlag(flag.Lookup("strategy"))
	runOne.Flags().AddGoFlag(flag.Lookup("use-network-proxy"))
	runOne.Flags().AddGoFlag(flag.Lookup("use-syscall-monitor"))
	runOne.Flags().AddGoFlag(flag.Lookup("ecosystem"))
	runOne.Flags().AddGoFlag(flag.Lookup("package"))
	runOne.Flags().AddGoFlag(flag.Lookup("version"))
	runOne.Flags().AddGoFlag(flag.Lookup("artifact"))

	getResults.Flags().AddGoFlag(flag.Lookup("run"))
	getResults.Flags().AddGoFlag(flag.Lookup("bench"))
	getResults.Flags().AddGoFlag(flag.Lookup("prefix"))
	getResults.Flags().AddGoFlag(flag.Lookup("pattern"))
	getResults.Flags().AddGoFlag(flag.Lookup("sample"))
	getResults.Flags().AddGoFlag(flag.Lookup("project"))
	getResults.Flags().AddGoFlag(flag.Lookup("clean"))
	getResults.Flags().AddGoFlag(flag.Lookup("format"))
	getResults.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	getResults.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	getResults.Flags().AddGoFlag(flag.Lookup("metadata-bucket"))

	export.Flags().AddGoFlag(flag.Lookup("run"))
	export.Flags().AddGoFlag(flag.Lookup("pattern"))
	export.Flags().AddGoFlag(flag.Lookup("project"))
	export.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	export.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	export.Flags().AddGoFlag(flag.Lookup("metadata-bucket"))
	export.Flags().AddGoFlag(flag.Lookup("asset-types"))
	export.Flags().AddGoFlag(flag.Lookup("destination"))
	export.Flags().AddGoFlag(flag.Lookup("rundex"))

	tui.Flags().AddGoFlag(flag.Lookup("project"))
	tui.Flags().AddGoFlag(flag.Lookup("llm-project"))
	tui.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	tui.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	tui.Flags().AddGoFlag(flag.Lookup("metadata-bucket"))
	tui.Flags().AddGoFlag(flag.Lookup("benchmark-dir"))
	tui.Flags().AddGoFlag(flag.Lookup("clean"))
	tui.Flags().AddGoFlag(flag.Lookup("def-dir"))
	tui.Flags().AddGoFlag(flag.Lookup("rundex-gcs-path"))
	tui.Flags().AddGoFlag(flag.Lookup("merged-asset-store"))

	listRuns.Flags().AddGoFlag(flag.Lookup("project"))
	listRuns.Flags().AddGoFlag(flag.Lookup("bench"))

	infer.Flags().AddGoFlag(flag.Lookup("api"))
	infer.Flags().AddGoFlag(flag.Lookup("format"))
	infer.Flags().AddGoFlag(flag.Lookup("ecosystem"))
	infer.Flags().AddGoFlag(flag.Lookup("package"))
	infer.Flags().AddGoFlag(flag.Lookup("version"))
	infer.Flags().AddGoFlag(flag.Lookup("artifact"))
	infer.Flags().AddGoFlag(flag.Lookup("bootstrap-bucket"))
	infer.Flags().AddGoFlag(flag.Lookup("bootstrap-version"))

	getGradleGAV.Flags().AddGoFlag(flag.Lookup("repository"))
	getGradleGAV.Flags().AddGoFlag(flag.Lookup("ref"))

	migrate.Flags().AddGoFlag(flag.Lookup("project"))
	migrate.Flags().AddGoFlag(flag.Lookup("dryrun"))

	getTrackedPackagesCmd.Flags().AddGoFlag(flag.Lookup("format"))

	setTrackedPackagesCmd.Flags().AddGoFlag(flag.Lookup("bench"))
	setTrackedPackagesCmd.Flags().AddGoFlag(flag.Lookup("format"))

	runAgent.Flags().AddGoFlag(flag.Lookup("api"))
	runAgent.Flags().AddGoFlag(flag.Lookup("project"))
	runAgent.Flags().AddGoFlag(flag.Lookup("ecosystem"))
	runAgent.Flags().AddGoFlag(flag.Lookup("package"))
	runAgent.Flags().AddGoFlag(flag.Lookup("version"))
	runAgent.Flags().AddGoFlag(flag.Lookup("artifact"))

	rootCmd.AddCommand(runBenchmark)
	rootCmd.AddCommand(runOne)
	rootCmd.AddCommand(getResults)
	rootCmd.AddCommand(export)
	rootCmd.AddCommand(tui)
	rootCmd.AddCommand(listRuns)
	rootCmd.AddCommand(infer)
	rootCmd.AddCommand(migrate)
	rootCmd.AddCommand(setTrackedPackagesCmd)
	rootCmd.AddCommand(getTrackedPackagesCmd)
	rootCmd.AddCommand(runAgent)
	rootCmd.AddCommand(getGradleGAV)
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
