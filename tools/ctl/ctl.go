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
	"sync"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/schema/form"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/firestore"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

var rootCmd = &cobra.Command{
	Use:   "ctl",
	Short: "A debugging tool for OSS-Rebuild",
}

func getExecutorVersion(ctx context.Context, client *http.Client, api *url.URL, service string) (string, error) {
	verURL := api.JoinPath("version")
	verURL.RawQuery = url.Values{"service": []string{service}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verURL.String(), nil)
	if err != nil {
		return "", errors.Wrap(err, "creating API version request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "sending API version request")
	}
	if resp.StatusCode != 200 {
		return "", errors.Wrap(errors.New(resp.Status), "API version request")
	}
	vb, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "reading API version")
	}
	return string(vb), nil
}

func readBenchmark(filename string) (ps benchmark.PackageSet, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&ps)
	return
}

func buildFetchRebuildRequest(ctx context.Context, bench, run, filter string, clean bool) (*firestore.FetchRebuildRequest, error) {
	var runs []string
	if run != "" {
		runs = strings.Split(run, ",")
	}
	req := firestore.FetchRebuildRequest{
		Runs: runs,
		Opts: firestore.FetchRebuildOpts{
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
		set, err := readBenchmark(bench)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		req.Bench = &set
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	return &req, nil
}

var tui = &cobra.Command{
	Use:   "tui --project <ID> [--debug-bucket <bucket>] [--clean]",
	Short: "A terminal UI for the OSS-Rebuild debugging tools",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		tctx := cmd.Context()
		if *debugBucket != "" {
			bucket, prefix, _ := strings.Cut(strings.TrimPrefix(*debugBucket, "gs://"), string(filepath.Separator))
			if prefix != "" {
				log.Fatalf("--debug-bucket cannot have additional path elements, found %s", prefix)
			}
			tctx = context.WithValue(tctx, rebuild.UploadArtifactsPathID, bucket)
		}
		// TODO: Support filtering in the UI on TUI.
		fireClient, err := firestore.NewClient(tctx, *project)
		if err != nil {
			log.Fatal(err)
		}
		tapp := ide.NewTuiApp(tctx, fireClient, firestore.FetchRebuildOpts{Clean: *clean})
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
		fireClient, err := firestore.NewClient(cmd.Context(), *project)
		if err != nil {
			log.Fatal(err)
		}
		rebuilds, err := fireClient.FetchRebuilds(cmd.Context(), req)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Fetched %d rebuilds", len(rebuilds))
		byCount := firestore.GroupRebuilds(rebuilds)
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
			var rbs []firestore.Rebuild
			for _, r := range rebuilds {
				rbs = append(rbs, r)
			}
			slices.SortFunc(rbs, func(a firestore.Rebuild, b firestore.Rebuild) int { return strings.Compare(a.ID(), b.ID()) })
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

type PackageWorker interface {
	Setup(ctx context.Context)
	ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict)
}

type Executor struct {
	Concurrency int
	Worker      PackageWorker
	Increment   func()
}

func (ex *Executor) Process(ctx context.Context, out chan schema.Verdict, packages []benchmark.Package) {
	ex.Worker.Setup(ctx)
	jobs := make(chan benchmark.Package)
	go func() {
		for _, p := range packages {
			jobs <- p
		}
		close(jobs)
	}()
	var wg sync.WaitGroup
	for i := 0; i < ex.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				ex.Worker.ProcessOne(ctx, p, out)
				if ex.Increment != nil {
					ex.Increment()
				}
			}
		}()
	}
	wg.Wait()
	close(out)
}

func makeHTTPRequest(ctx context.Context, u *url.URL, msg schema.Message) *http.Request {
	values, err := form.Marshal(msg)
	if err != nil {
		log.Fatal(errors.Wrap(err, "creating values"))
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		log.Fatal(errors.Wrap(err, "creating request"))
	}
	return req
}

type WorkerConfig struct {
	client   *http.Client
	url      *url.URL
	limiters map[string]<-chan time.Time
	run      string
}

type AttestWorker struct {
	WorkerConfig
}

func (w *AttestWorker) Setup(ctx context.Context) {}

func (w *AttestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	for _, v := range p.Versions {
		<-w.limiters[p.Ecosystem]
		resp, err := w.client.Do(makeHTTPRequest(ctx, w.url.JoinPath("rebuild"), &schema.RebuildPackageRequest{
			Ecosystem: rebuild.Ecosystem(p.Ecosystem),
			Package:   p.Name,
			Version:   v,
			ID:        w.run,
		}))
		var errMsg string
		if err != nil {
			errMsg = errors.Wrap(err, "sending request").Error()
		} else if resp.StatusCode != 200 {
			errMsg = errors.Wrapf(errors.New(resp.Status), "sending request").Error()
		}
		var verdict schema.Verdict
		if errMsg != "" {
			verdict = schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		} else {
			// TODO: Once the attestation endpoint returns verdict objects,
			// support that here.
			verdict = schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: "",
			}
		}
		out <- verdict
	}
}

type SmoketestWorker struct {
	WorkerConfig
	warmup bool
}

func (w *SmoketestWorker) Setup(ctx context.Context) {
	if w.warmup {
		// First, warm up the instances to ensure it can handle actual load.
		// Warm up requires the service fulfill sequentially successful version
		// requests (which hit both the API and the builder jobs).
		for i := 0; i < 5; {
			_, err := getExecutorVersion(ctx, w.client, w.url, "build-local")
			if err != nil {
				i = 0
			} else {
				i++
			}
		}
	}
}

func (w *SmoketestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	<-w.limiters[p.Ecosystem]
	resp, err := w.client.Do(makeHTTPRequest(ctx, w.url.JoinPath("smoketest"), &schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(p.Ecosystem),
		Package:   p.Name,
		Versions:  p.Versions,
		ID:        w.run,
	}))
	var errMsg string
	if err != nil {
		errMsg = errors.Wrap(err, "sending request").Error()
	}
	if resp.StatusCode != 200 {
		errMsg = errors.Wrapf(errors.New(resp.Status), "sending request").Error()
	}
	if errMsg != "" {
		for _, v := range p.Versions {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		}
	} else {
		var r schema.SmoketestResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			log.Fatalf("Failed to decode smoketest response: %v", err)
		}
		for _, v := range r.Verdicts {
			out <- v
		}
	}
}

func defaultLimiters() map[string]<-chan time.Time {
	return map[string]<-chan time.Time{
		"pypi":  time.Tick(time.Second),
		"npm":   time.Tick(2 * time.Second),
		"maven": time.Tick(2 * time.Second),
		// NOTE: cratesio needs to be especially slow given our registry API
		// constraint of 1QPS. At minimum, we expect to make 4 calls per test.
		"cratesio": time.Tick(8 * time.Second),
	}
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
		mode := firestore.BenchmarkMode(args[0])
		if mode != firestore.SmoketestMode && mode != firestore.AttestMode {
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
			set, err = readBenchmark(path)
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
		var executor string
		if mode == firestore.SmoketestMode {
			executor, err = getExecutorVersion(ctx, client, apiURL, "build-local")
		} else if mode == firestore.AttestMode {
			// Empty string returns the API version.
			executor, err = getExecutorVersion(ctx, client, apiURL, "")
		}
		if err != nil {
			log.Fatal(err)
		}
		var run string
		if *buildLocal {
			// The build-local service does not support creating a new run-id.
			// If we're talking to build-local directly, then we skip run-id generation.
			run = time.Now().UTC().Format(time.RFC3339)
		} else {
			stub := api.Stub[schema.CreateRunRequest, schema.CreateRunResponse](client, *apiURL.JoinPath("runs"))
			resp, err := stub(ctx, schema.CreateRunRequest{
				Name: filepath.Base(args[1]),
				Hash: hex.EncodeToString(set.Hash(sha256.New())),
				Type: string(mode),
			})
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating run"))
			}
			run = resp.ID
		}
		conf := WorkerConfig{
			client:   client,
			url:      apiURL,
			limiters: defaultLimiters(),
			run:      run,
		}
		bar := pb.New(len(set.Packages))
		bar.Output = cmd.OutOrStderr()
		bar.ShowTimeLeft = true
		ex := Executor{Concurrency: *maxConcurrency, Increment: func() { bar.Increment() }}
		if mode == firestore.SmoketestMode {
			ex.Worker = &SmoketestWorker{
				WorkerConfig: conf,
				warmup:       isCloudRun(apiURL),
			}
		} else {
			ex.Worker = &AttestWorker{
				WorkerConfig: conf,
			}
		}
		verdictChan := make(chan schema.Verdict)
		log.Printf("Triggering rebuilds on executor version '%s' with ID=%s...\n", executor, run)
		bar.Start()
		go ex.Process(ctx, verdictChan, set.Packages)
		var verdicts []schema.Verdict
		for v := range verdictChan {
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
		mode := firestore.BenchmarkMode(args[0])
		if mode != firestore.SmoketestMode && mode != firestore.AttestMode {
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
		var strategy *schema.StrategyOneOf
		if *strategyPath != "" {
			if mode == firestore.AttestMode {
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
		if *ecosystem == "" || *pkg == "" || *version == "" {
			log.Fatal("ecosystem, package, and version must be provided")
		}
		var req *http.Request
		if mode == firestore.SmoketestMode {
			req = makeHTTPRequest(ctx, apiURL.JoinPath("smoketest"), &schema.SmoketestRequest{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Versions:  []string{*version},
				Strategy:  strategy,
				ID:        "runOne",
			})
		} else if mode == firestore.AttestMode {
			req = makeHTTPRequest(ctx, apiURL.JoinPath("rebuild"), &schema.RebuildPackageRequest{
				Ecosystem:        rebuild.Ecosystem(*ecosystem),
				Package:          *pkg,
				Version:          *version,
				StrategyFromRepo: *useStrategyRepo,
				ID:               "runOne",
			})
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Fatal(err.Error())
		}
		if resp.StatusCode != 200 {
			log.Fatalf("response status: %v", *resp)
		}
		io.WriteString(cmd.OutOrStdout(), fmt.Sprintf("Received response status: %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode)))
		io.Copy(cmd.OutOrStdout(), resp.Body)
		cmd.OutOrStdout().Write([]byte("\n"))
	},
}

var listRuns = &cobra.Command{
	Use:   "list-runs -project <ID> [ -bench <benchmark.json> ]",
	Short: "List runs",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		var opts firestore.FetchRunsOpts
		if *bench != "" {
			log.Printf("Extracting benchmark %s...\n", filepath.Base(*bench))
			set, err := readBenchmark(*bench)
			if err != nil {
				log.Fatal(errors.Wrap(err, "reading benchmark file"))
			}
			log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
			opts.BenchmarkHash = hex.EncodeToString(set.Hash(sha256.New()))
		}
		if *project == "" {
			log.Fatal("project not provided")
		}
		client, err := firestore.NewClient(ctx, *project)
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

var (
	// Shared
	apiUri = flag.String("api", "", "OSS Rebuild API endpoint URI")
	// run-bench
	maxConcurrency = flag.Int("max-concurrency", 90, "maximum number of inflight requests")
	buildLocal     = flag.Bool("local", false, "true if this request is going direct to build-local (not through API first)")
	// get-results
	runFlag         = flag.String("run", "", "the run(s) from which to fetch results")
	bench           = flag.String("bench", "", "a path to a benchmark file. if provided, only results from that benchmark will be fetched")
	format          = flag.String("format", "summary", "the format to be printed. Options: summary, bench")
	filter          = flag.String("filter", "", "a verdict message (or prefix) which will restrict the returned results")
	sample          = flag.Int("sample", -1, "if provided, only N results will be displayed")
	project         = flag.String("project", "", "the project from which to fetch the Firestore data")
	clean           = flag.Bool("clean", false, "whether to apply normalization heuristics to group similar verdicts")
	debugBucket     = flag.String("debug-bucket", "", "the gcs bucket to find debug logs and artifacts")
	strategyPath    = flag.String("strategy", "", "the strategy file to use")
	useStrategyRepo = flag.Bool("strategy-from-repo", false, "whether to lookup and use the strategy from the server-configured repo")

	ecosystem = flag.String("ecosystem", "", "the ecosystem")
	pkg       = flag.String("package", "", "the package name")
	version   = flag.String("version", "", "the version of the package")
	artifact  = flag.String("artifact", "", "the artifact name")
)

func init() {
	runBenchmark.Flags().AddGoFlag(flag.Lookup("api"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("max-concurrency"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("local"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("format"))

	runOne.Flags().AddGoFlag(flag.Lookup("api"))
	runOne.Flags().AddGoFlag(flag.Lookup("strategy"))
	runOne.Flags().AddGoFlag(flag.Lookup("strategy-from-repo"))
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
	tui.Flags().AddGoFlag(flag.Lookup("clean"))
	tui.Flags().AddGoFlag(flag.Lookup("debug-bucket"))

	listRuns.Flags().AddGoFlag(flag.Lookup("project"))
	listRuns.Flags().AddGoFlag(flag.Lookup("bench"))

	rootCmd.AddCommand(runBenchmark)
	rootCmd.AddCommand(runOne)
	rootCmd.AddCommand(getResults)
	rootCmd.AddCommand(tui)
	rootCmd.AddCommand(listRuns)
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
