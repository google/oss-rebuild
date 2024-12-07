// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
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
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

var rootCmd = &cobra.Command{
	Use:   "ctl",
	Short: "A debugging tool for OSS-Rebuild",
}

func buildFetchRebuildRequest(ctx context.Context, bench, run, filter string, clean bool) (*rundex.FetchRebuildRequest, error) {
	var runs []string
	if run != "" {
		runs = strings.Split(run, ",")
	}
	req := rundex.FetchRebuildRequest{
		Runs: runs,
		Opts: rundex.FetchRebuildOpts{
			Filter: filter,
			Clean:  clean,
		},
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
	Use:   "tui [--project <ID>] [--debug-storage <bucket>] [--benchmark-dir <dir>] [--clean]",
	Short: "A terminal UI for the OSS-Rebuild debugging tools",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		// Exactly one of benchmarkDir or project should be set.
		if (*benchmarkDir != "") == (*project != "" || *debugStorage != "") {
			log.Fatal(errors.New("TUI should either be local (--benchmark-dir) or remote (--project, --debug-storage)"))
		}
		tctx := cmd.Context()
		var fireClient rundex.Reader
		if *benchmarkDir != "" {
			fireClient = rundex.NewLocalClient(localfiles.Rundex())
			tctx = context.WithValue(tctx, rebuild.DebugStoreID, "file://"+localfiles.AssetsPath())
		} else {
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
				tctx = context.WithValue(tctx, rebuild.DebugStoreID, *debugStorage)
			}
			if *logsBucket != "" {
				tctx = context.WithValue(tctx, rebuild.LogsBucketID, *logsBucket)
			}
			// TODO: Support filtering in the UI on TUI.
			var err error
			fireClient, err = rundex.NewFirestore(tctx, *project)
			if err != nil {
				log.Fatal(err)
			}
		}
		tapp := ide.NewTuiApp(tctx, fireClient, rundex.FetchRebuildOpts{Clean: *clean}, *benchmarkDir)
		if err := tapp.Run(); err != nil {
			// TODO: This cleanup will be unnecessary once NewTuiApp does split logging.
			log.Default().SetOutput(os.Stdout)
			log.Fatal(err)
		}
	},
}

var getResults = &cobra.Command{
	Use:   "get-results -project <ID> -run <ID> [-bench <benchmark.json>] [-filter <verdict>] [-sample N]",
	Short: "Analyze rebuild results",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		req, err := buildFetchRebuildRequest(cmd.Context(), *bench, *runFlag, *filter, *clean)
		if err != nil {
			log.Fatal(err)
		}
		if *format == "summary" && *sample > 0 {
			log.Fatal("--sample option incompatible with --format=summary")
		}
		fireClient, err := rundex.NewFirestore(cmd.Context(), *project)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Querying results for [executors=%v,runs=%v,bench=%s,filter=%s]", req.Executors, req.Runs, *bench, req.Opts.Filter)
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
		case "summary":
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
			}
			ps.Updated = time.Now()
			b, err := json.MarshalIndent(ps, "", "  ")
			if err != nil {
				log.Fatal(errors.Wrap(err, "marshalling benchmark"))
			}
			fmt.Println(string(b))
		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
	},
}

func isCloudRun(u *url.URL) bool {
	return strings.HasSuffix(u.Host, ".run.app")
}

var runBenchmark = &cobra.Command{
	Use:   "run-bench smoketest|attest -api <URI>  [-local] [-format=summary|csv] <benchmark.json>",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		mode := benchmark.BenchmarkMode(args[0])
		if mode != benchmark.SmoketestMode && mode != benchmark.AttestMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
		}
		if *apiUri == "" {
			log.Fatal("API endpoint not provided")
		}
		apiURL, err := url.Parse(*apiUri)
		if err != nil {
			log.Fatal(errors.Wrap(err, "parsing API endpoint"))
		}
		var set benchmark.PackageSet
		{
			path := args[1]
			log.Printf("Extracting benchmark %s...\n", filepath.Base(path))
			set, err = benchmark.ReadBenchmark(path)
			if err != nil {
				log.Fatal(errors.Wrap(err, "reading benchmark file"))
			}
			log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
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
		var run string
		if *buildLocal {
			run = time.Now().UTC().Format(time.RFC3339)
		} else {
			stub := api.Stub[schema.CreateRunRequest, schema.Run](client, *apiURL.JoinPath("runs"))
			resp, err := stub(ctx, schema.CreateRunRequest{
				BenchmarkName: filepath.Base(args[1]),
				BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
				Type:          string(mode),
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating run"))
			}
			run = resp.ID
		}
		bar := pb.New(set.Count)
		bar.Output = cmd.OutOrStderr()
		bar.ShowTimeLeft = true
		verdictChan, err := benchmark.RunBench(ctx, client, apiURL, set, benchmark.RunBenchOpts{
			Mode:           mode,
			RunID:          run,
			MaxConcurrency: *maxConcurrency,
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
			verdicts = append(verdicts, v)
		}
		bar.Finish()
		sort.Slice(verdicts, func(i, j int) bool {
			return fmt.Sprint(verdicts[i].Target) > fmt.Sprint(verdicts[j].Target)
		})
		switch *format {
		// TODO: Maybe add more format options, or include more data in the csv?
		case "csv":
			w := csv.NewWriter(cmd.OutOrStdout())
			defer w.Flush()
			for _, v := range verdicts {
				if err := w.Write([]string{fmt.Sprintf("%v", v.Target), v.Message}); err != nil {
					log.Fatal(errors.Wrap(err, "writing CSV"))
				}
			}
		case "summary":
			var successes int
			for _, v := range verdicts {
				if v.Message == "" {
					successes++
				}
			}
			io.WriteString(cmd.OutOrStdout(), fmt.Sprintf("Successes: %d/%d\n", successes, len(verdicts)))
		default:
			log.Fatalf("Unsupported format: %s", *format)
		}
	},
}

var runOne = &cobra.Command{
	Use:   "run-one smoketest|attest --api <URI> --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--strategy <strategy.yaml>] [--strategy-from-repo]",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if *ecosystem == "" || *pkg == "" || *version == "" {
			log.Fatal("ecosystem, package, and version must be provided")
		}
		mode := benchmark.BenchmarkMode(args[0])
		if mode != benchmark.SmoketestMode && mode != benchmark.AttestMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
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
				if mode == benchmark.AttestMode {
					log.Fatal("--strategy not supported in attest mode, use --strategy-from-repo")
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
		var verdicts []schema.Verdict
		{
			if mode == benchmark.SmoketestMode {
				stub := api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](client, *apiURL.JoinPath("smoketest"))
				resp, err := stub(ctx, schema.SmoketestRequest{
					Ecosystem: rebuild.Ecosystem(*ecosystem),
					Package:   *pkg,
					Versions:  []string{*version},
					Strategy:  strategy,
				})
				if err != nil {
					log.Fatal(errors.Wrap(err, "running smoketest"))
				}
				verdicts = resp.Verdicts
			} else {
				stub := api.Stub[schema.RebuildPackageRequest, schema.Verdict](client, *apiURL.JoinPath("rebuild"))
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
				verdicts = []schema.Verdict{*resp}
			}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		for _, v := range verdicts {
			if err := enc.Encode(v); err != nil {
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

var infer = &cobra.Command{
	Use:   "infer --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--api <URI>] ",
	Short: "Run inference",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		req := schema.InferenceRequest{
			Ecosystem: rebuild.Ecosystem(*ecosystem),
			Package:   *pkg,
			Version:   *version,
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
			stub := api.Stub[schema.InferenceRequest, schema.StrategyOneOf](client, *apiURL.JoinPath("runs"))
			resp, err = stub(cmd.Context(), req)
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating run"))
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
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			log.Fatal(errors.Wrap(err, "encoding result"))
		}
	},
}

var (
	// Shared
	apiUri     = flag.String("api", "", "OSS Rebuild API endpoint URI")
	ecosystem  = flag.String("ecosystem", "", "the ecosystem")
	pkg        = flag.String("package", "", "the package name")
	version    = flag.String("version", "", "the version of the package")
	artifact   = flag.String("artifact", "", "the artifact name")
	verbose    = flag.Bool("v", false, "verbose output")
	logsBucket = flag.String("logs-bucket", "", "the gcs bucket where gcb logs are stored")
	// run-bench
	maxConcurrency = flag.Int("max-concurrency", 90, "maximum number of inflight requests")
	buildLocal     = flag.Bool("local", false, "true if this request is going direct to build-local (not through API first)")
	// run-one
	strategyPath      = flag.String("strategy", "", "the strategy file to use")
	useNetworkProxy   = flag.Bool("use-network-proxy", false, "request the newtwork proxy")
	useSyscallMonitor = flag.Bool("use-syscall-monitor", false, "request the newtwork proxy")
	// get-results
	runFlag      = flag.String("run", "", "the run(s) from which to fetch results")
	bench        = flag.String("bench", "", "a path to a benchmark file. if provided, only results from that benchmark will be fetched")
	format       = flag.String("format", "summary", "the format to be printed. Options: summary, bench")
	filter       = flag.String("filter", "", "a verdict message (or prefix) which will restrict the returned results")
	sample       = flag.Int("sample", -1, "if provided, only N results will be displayed")
	project      = flag.String("project", "", "the project from which to fetch the Firestore data")
	clean        = flag.Bool("clean", false, "whether to apply normalization heuristics to group similar verdicts")
	debugStorage = flag.String("debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	//TUI
	benchmarkDir = flag.String("benchmark-dir", "", "a directory with benchmarks to work with")
)

func init() {
	runBenchmark.Flags().AddGoFlag(flag.Lookup("api"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("max-concurrency"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("local"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("format"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("v"))

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
	getResults.Flags().AddGoFlag(flag.Lookup("filter"))
	getResults.Flags().AddGoFlag(flag.Lookup("sample"))
	getResults.Flags().AddGoFlag(flag.Lookup("project"))
	getResults.Flags().AddGoFlag(flag.Lookup("clean"))
	getResults.Flags().AddGoFlag(flag.Lookup("format"))

	tui.Flags().AddGoFlag(flag.Lookup("project"))
	tui.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	tui.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	tui.Flags().AddGoFlag(flag.Lookup("benchmark-dir"))
	tui.Flags().AddGoFlag(flag.Lookup("clean"))

	listRuns.Flags().AddGoFlag(flag.Lookup("project"))
	listRuns.Flags().AddGoFlag(flag.Lookup("bench"))

	infer.Flags().AddGoFlag(flag.Lookup("ecosystem"))
	infer.Flags().AddGoFlag(flag.Lookup("package"))
	infer.Flags().AddGoFlag(flag.Lookup("version"))
	infer.Flags().AddGoFlag(flag.Lookup("artifact"))

	rootCmd.AddCommand(runBenchmark)
	rootCmd.AddCommand(runOne)
	rootCmd.AddCommand(getResults)
	rootCmd.AddCommand(tui)
	rootCmd.AddCommand(listRuns)
	rootCmd.AddCommand(infer)
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
