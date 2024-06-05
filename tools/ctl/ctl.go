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
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/firestore"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
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
		byCount := firestore.GroupRebuilds(rebuilds)
		if len(byCount) == 0 {
			log.Println("No results")
			return
		}
		switch *format {
		case "summary":
			log.Println("Verdict summary:")
			for _, vg := range byCount {
				s := fmt.Sprintf(" %4d - %s (example: %s)\n", vg.Count, vg.Msg[:min(len(vg.Msg), 1000)], vg.Examples[0].ID())
				cmd.OutOrStdout().Write([]byte(s))
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
			cmd.OutOrStdout().Write(b)
			cmd.OutOrStderr().Write([]byte("\n"))
		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
	},
}

var missingAttestations = &cobra.Command{
	Use:   "find-missing-attestations -project <ID> -run <ID> -attestation-bucket <name> -format <summary|bench>",
	Short: "Find successes from a smoketest run that don't exist in the attestation bucket.",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		log.Default().SetOutput(cmd.ErrOrStderr())
		ctx := cmd.Context()
		var passing []firestore.Rebuild
		{
			req, err := buildFetchRebuildRequest(ctx, "", *runFlag, "", *clean)
			if err != nil {
				log.Fatal(err)
			}
			fireClient, err := firestore.NewClient(ctx, *project)
			if err != nil {
				log.Fatal(err)
			}
			rebuilds, err := fireClient.FetchRebuilds(ctx, req)
			if err != nil {
				log.Fatal(err)
			}
			for _, r := range rebuilds {
				if r.Success {
					passing = append(passing, r)
				}
			}
		}
		var missing []firestore.Rebuild
		{
			ctx = context.WithValue(ctx, rebuild.RunID, "")
			ctx = context.WithValue(ctx, rebuild.GCSClientOptionsID, []option.ClientOption{option.WithoutAuthentication()})
			attestation, err := rebuild.NewGCSStore(ctx, "gs://"+*attestationBucket)
			if err != nil {
				log.Fatal(errors.Wrapf(err, "creating attestation store"))
			}
			log.Println("Checking all successful packages for attestation...")
			bar := pb.New(len(passing))
			bar.ShowTimeLeft = true
			bar.Output = cmd.ErrOrStderr()
			bar.Start()
			for _, rb := range passing {
				r, _, err := attestation.Reader(ctx, rebuild.Asset{Target: rb.Target(), Type: rebuild.AttestationBundleAsset})
				if errors.Is(err, rebuild.ErrAssetNotFound) {
					missing = append(missing, rb)
				} else if err != nil {
					log.Fatal(errors.Wrapf(err, "failed attempting to read %v", rb.Target()))
				} else {
					defer r.Close()
				}
				bar.Increment()
			}
			bar.Finish()
		}
		slices.SortFunc(missing, func(a firestore.Rebuild, b firestore.Rebuild) int { return strings.Compare(a.ID(), b.ID()) })
		switch *format {
		case "summary":
			for _, rb := range missing {
				cmd.OutOrStdout().Write([]byte(fmt.Sprintf("%v", rb.Target())))
			}
		case "bench":
			var ps benchmark.PackageSet
			ps.Count = len(missing)
			for _, r := range missing {
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
			cmd.OutOrStdout().Write(b)
			cmd.OutOrStderr().Write([]byte("\n"))
		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
	},
}

func makeHTTPRequest(ctx context.Context, u *url.URL, msg schema.Message) *http.Request {
	values, err := msg.ToValues()
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

var runBenchmark = &cobra.Command{
	Use:   "run-bench smoketest|attest -api <URI> <benchmark.json>",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		mode := firestore.BenchmarkMode(args[0])
		if mode != firestore.SmoketestMode && mode != firestore.AttestMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
		}
		path := args[1]
		log.Printf("Extracting benchmark %s...\n", filepath.Base(path))
		set, err := readBenchmark(path)
		if err != nil {
			log.Fatal(errors.Wrap(err, "reading benchmark file"))
		}
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
		if *api == "" {
			log.Fatal("API endpoint not provided")
		}
		apiURL, err := url.Parse(*api)
		if err != nil {
			log.Fatal(errors.Wrap(err, "parsing API endpoint"))
		}
		apiURL.Scheme = "https"
		ctx := cmd.Context()
		idclient, err := oauth.AuthorizedUserIDClient(ctx)
		if err != nil {
			log.Fatal(errors.Wrap(err, "creating authorized HTTP client"))
		}
		var executor string
		if mode == firestore.SmoketestMode {
			executor, err = getExecutorVersion(ctx, idclient, apiURL, "build-local")
		} else if mode == firestore.AttestMode {
			// Empty string returns the API version.
			executor, err = getExecutorVersion(ctx, idclient, apiURL, "")
		}
		if err != nil {
			log.Fatal(err)
		}
		var run string
		{
			u := apiURL.JoinPath("runs")
			values := url.Values{
				"name": []string{filepath.Base(path)},
				"hash": []string{hex.EncodeToString(set.Hash(sha256.New()))},
				"type": []string{string(mode)},
			}
			u.RawQuery = values.Encode()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
			if err != nil {
				log.Fatal(errors.Wrap(err, "creating API version request"))
			}
			resp, err := idclient.Do(req)
			if err != nil {
				log.Fatal(errors.Wrap(err, "requesting run creation"))
			}
			if resp.StatusCode != 200 {
				log.Fatal(errors.Wrap(errors.New(resp.Status), "creating run"))
			}
			runBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatal(errors.Wrap(err, "reading created run"))
			}
			run = string(runBytes)
		}
		log.Printf("Triggering rebuilds on executor version '%s' with ID=%s...\n", executor, run)
		jobs := make(chan benchmark.Package, *maxConcurrency)
		bar := pb.StartNew(len(set.Packages))
		bar.ShowTimeLeft = true
		go func() {
			for _, p := range set.Packages {
				jobs <- p
			}
			close(jobs)
		}()
		limiterMap := map[string]<-chan time.Time{
			"pypi":  time.Tick(time.Second),
			"npm":   time.Tick(2 * time.Second),
			"maven": time.Tick(2 * time.Second),
			// NOTE: cratesio needs to be especially slow given our registry API
			// constraint of 1QPS. At minimum, we expect to make 4 calls per test.
			"cratesio": time.Tick(8 * time.Second),
		}
		var totalErrors int
		var aggErrors []string
		var wg sync.WaitGroup
		for i := 0; i < *maxConcurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if mode == firestore.SmoketestMode {
					// First, warm up the instances to ensure it can handle actual load.
					// Warm up requires the service fulfill sequentially successful version
					// requests (which hit both the API and the builder jobs).
					for i := 0; i < 5; {
						_, err := getExecutorVersion(ctx, idclient, apiURL, "build-local")
						if err != nil {
							i = 0
						} else {
							i++
						}
					}
				}
				// Second, start triggering rebuilds.
				for j := range jobs {
					var reqs []*http.Request
					if mode == firestore.SmoketestMode {
						reqs = append(reqs, makeHTTPRequest(ctx, apiURL.JoinPath("smoketest"), schema.SmoketestRequest{
							Ecosystem: rebuild.Ecosystem(j.Ecosystem),
							Package:   j.Name,
							Versions:  j.Versions,
							ID:        run,
						}))
					} else if mode == firestore.AttestMode {
						for _, v := range j.Versions {
							reqs = append(reqs, makeHTTPRequest(ctx, apiURL.JoinPath("rebuild"), schema.RebuildPackageRequest{
								Ecosystem: rebuild.Ecosystem(j.Ecosystem),
								Package:   j.Name,
								Version:   v,
								ID:        run,
							}))
						}
					}
					for _, req := range reqs {
						// Wait for a tick from the limiter.
						<-limiterMap[j.Ecosystem]
						resp, err := idclient.Do(req)
						if err != nil {
							totalErrors++
							aggErrors = append(aggErrors, errors.Wrap(err, "sending request").Error())
							continue
						}
						if resp.StatusCode != 200 {
							totalErrors++
							aggErrors = append(aggErrors, errors.Wrap(errors.New(resp.Status), "request").Error())
						}
					}
					bar.Increment()
				}
			}()
		}
		wg.Wait()
		bar.Finish()
		log.Printf("Completed rebuilds for %d artifacts...\n", set.Count)
		log.Printf("Total errors: %d\n", totalErrors)
		for _, e := range aggErrors {
			log.Println(e)
		}
	},
}

var runOne = &cobra.Command{
	Use:   "run-one smoketest|attest --api <URI> --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--strategy <strategy.yaml>]",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mode := firestore.BenchmarkMode(args[0])
		if mode != firestore.SmoketestMode && mode != firestore.AttestMode {
			log.Fatalf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
		}
		if *api == "" {
			log.Fatal("API endpoint not provided")
		}
		apiURL, err := url.Parse(*api)
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
			req = makeHTTPRequest(ctx, apiURL.JoinPath("smoketest"), schema.SmoketestRequest{
				Ecosystem:     rebuild.Ecosystem(*ecosystem),
				Package:       *pkg,
				Versions:      []string{*version},
				StrategyOneof: strategy,
				ID:            "runOne",
			})
		} else if mode == firestore.AttestMode {
			req = makeHTTPRequest(ctx, apiURL.JoinPath("rebuild"), schema.RebuildPackageRequest{
				Ecosystem:     rebuild.Ecosystem(*ecosystem),
				Package:       *pkg,
				Version:       *version,
				StrategyOneof: strategy,
				ID:            "runOne",
			})
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Fatal(err.Error())
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
	api     = flag.String("api", "", "OSS Rebuild API endpoint URI")
	project = flag.String("project", "", "the project from which to fetch the Firestore data")
	// tui
	debugBucket = flag.String("debug-bucket", "", "the gcs bucket to find debug logs and artifacts")
	// run-bench
	maxConcurrency = flag.Int("max-concurrency", 90, "maximum number of inflight requests")
	// run-one
	ecosystem    = flag.String("ecosystem", "", "the ecosystem")
	pkg          = flag.String("package", "", "the package name")
	version      = flag.String("version", "", "the version of the package")
	artifact     = flag.String("artifact", "", "the artifact name")
	strategyPath = flag.String("strategy", "", "the strategy file to use")
	// get-results and find-missing-attestations
	runFlag = flag.String("run", "", "the run(s) from which to fetch results")
	format  = flag.String("format", "summary", "the format to be printed. Options: summary, bench")
	// get-results
	bench  = flag.String("bench", "", "a path to a benchmark file. if provided, only results from that benchmark will be fetched")
	filter = flag.String("filter", "", "a verdict message (or prefix) which will restrict the returned results")
	sample = flag.Int("sample", -1, "if provided, only N results will be displayed")
	clean  = flag.Bool("clean", false, "whether to apply normalization heuristics to group similar verdicts")
	// find-missing-attestations
	attestationBucket = flag.String("attestation-bucket", "google-rebuild-attestations", "GCS bucket from which to pull rebuild attestations")
)

func init() {
	runBenchmark.Flags().AddGoFlag(flag.Lookup("api"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("max-concurrency"))

	runOne.Flags().AddGoFlag(flag.Lookup("api"))
	runOne.Flags().AddGoFlag(flag.Lookup("strategy"))
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

	missingAttestations.Flags().AddGoFlag(flag.Lookup("project"))
	missingAttestations.Flags().AddGoFlag(flag.Lookup("run"))
	missingAttestations.Flags().AddGoFlag(flag.Lookup("attestation-bucket"))
	missingAttestations.Flags().AddGoFlag(flag.Lookup("format"))

	tui.Flags().AddGoFlag(flag.Lookup("project"))
	tui.Flags().AddGoFlag(flag.Lookup("clean"))
	tui.Flags().AddGoFlag(flag.Lookup("debug-bucket"))

	listRuns.Flags().AddGoFlag(flag.Lookup("project"))
	listRuns.Flags().AddGoFlag(flag.Lookup("bench"))

	rootCmd.AddCommand(runBenchmark)
	rootCmd.AddCommand(runOne)
	rootCmd.AddCommand(getResults)
	rootCmd.AddCommand(missingAttestations)
	rootCmd.AddCommand(tui)
	rootCmd.AddCommand(listRuns)
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
