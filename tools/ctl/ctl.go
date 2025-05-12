// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
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
	"text/template"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"cloud.google.com/go/vertexai/genai"
	"github.com/cheggaaa/pb"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/benchmark/run"
	"github.com/google/oss-rebuild/tools/ctl/ide"
	"github.com/google/oss-rebuild/tools/ctl/ide/assistant"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/migrations"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/iterator"
	"gopkg.in/yaml.v3"
)

var rootCmd = &cobra.Command{
	Use:   "ctl",
	Short: "A debugging tool for OSS-Rebuild",
}

var debuildShellScript = template.Must(
	template.New(
		"rebuild shell script",
	).Funcs(template.FuncMap{
		"join": func(sep string, s []string) string { return strings.Join(s, sep) },
	}).Parse(
		textwrap.Dedent(`
# Install dependencies.
set -eux
apt update
apt install -y {{join " " .SystemDeps}}

mkdir /src && cd /src
{{.Source}}
{{.Deps}}

# Run the build.
{{.Build}}
mkdir /out && cp /src/{{.OutputPath}} /out/
`)[1:], // remove leading newline
	))

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

func makeShellScript(input rebuild.Input) (string, error) {
	env := rebuild.BuildEnv{HasRepo: false, PreferPreciseToolchain: true}
	instructions, err := input.Strategy.GenerateFor(input.Target, env)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate strategy")
	}
	shellScript := new(bytes.Buffer)
	if input.Target.Ecosystem == rebuild.Debian {
		err = debuildShellScript.Execute(shellScript, instructions)
	} else {
		err = errors.Errorf("unimplemented ecosystem %v", input.Target.Ecosystem)
	}
	if err != nil {
		return "", errors.Wrap(err, "populating template")
	}
	return shellScript.String(), nil
}

var tui = &cobra.Command{
	Use:   "tui [--project <ID>] [--debug-storage <bucket>] [--benchmark-dir <dir>] [--clean] [--llm-project]",
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
		{
			// Prefer the firestore based rundex where possible, local otherwise.
			// NOTE: We may eventually want to support firestore as a starting point, then local for quick debugging after that.
			if *project != "" {
				var err error
				dex, err = rundex.NewFirestore(cmd.Context(), *project)
				if err != nil {
					log.Fatal(err)
				}
			} else {
				dex = rundex.NewLocalClient(localfiles.Rundex())
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
		}
		butler := localfiles.NewButler(*metadataBucket, *logsBucket, *debugStorage, mux)
		aiClient, err := genai.NewClient(cmd.Context(), *llmProject, "us-central1")
		if err != nil {
			log.Fatal(errors.Wrap(err, "failed to create a genai client"))
		}
		asst := assistant.NewAssistant(butler, aiClient)
		benches := benchmark.NewFSRepository(osfs.New(*benchmarkDir))
		tapp := ide.NewTuiApp(dex, rundex.FetchRebuildOpts{Clean: *clean}, benches, buildDefs, butler, asst)
		if err := tapp.Run(cmd.Context()); err != nil {
			// TODO: This cleanup will be unnecessary once NewTuiApp does split logging.
			log.Default().SetOutput(os.Stdout)
			log.Fatal(err)
		}
	},
}

var getResults = &cobra.Command{
	Use:   "get-results -project <ID> -run <ID> [-bench <benchmark.json>] [-prefix <prefix>] [-pattern <regex>] [-sample N] [-format=summary|bench|assets] [-asset=<assetType>]",
	Short: "Analyze rebuild results",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		req, err := buildFetchRebuildRequest(*bench, *runFlag, *prefix, *pattern, *clean, true)
		if err != nil {
			log.Fatal(err)
		}
		if (*format == "" || *format == "summary") && *sample > 0 {
			log.Fatal("--sample option incompatible with --format=summary")
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
		case "assets":
			regclient := http.DefaultClient
			mux := rebuild.RegistryMux{
				Debian:   debianreg.HTTPRegistry{Client: regclient},
				CratesIO: cratesreg.HTTPRegistry{Client: regclient},
				NPM:      npmreg.HTTPRegistry{Client: regclient},
				PyPI:     pypireg.HTTPRegistry{Client: regclient},
			}
			butler := localfiles.NewButler(*metadataBucket, *logsBucket, *debugStorage, mux)
			atype := rebuild.AssetType(*assetType)
			ctx := cmd.Context()
			for _, r := range rebuilds {
				path, err := butler.Fetch(ctx, *runFlag, r.WasSmoketest(), atype.For(r.Target()))
				if err != nil {
					cmd.OutOrStderr().Write([]byte(err.Error() + "\n"))
					continue
				}
				cmd.OutOrStdout().Write([]byte(path + "\n"))
			}
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
		mode := schema.ExecutionMode(args[0])
		if mode != schema.SmoketestMode && mode != schema.AttestMode {
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
		var runID string
		var dex rundex.Writer
		if *buildLocal {
			now := time.Now().UTC()
			runID = now.Format(time.RFC3339)
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
		verdictChan, err := run.RunBench(ctx, client, apiURL, set, run.RunBenchOpts{
			Mode:              mode,
			RunID:             runID,
			MaxConcurrency:    *maxConcurrency,
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

var runOne = &cobra.Command{
	Use:   "run-one smoketest|attest --api <URI> --ecosystem <ecosystem> --package <name> --version <version> [--artifact <name>] [--strategy <strategy.yaml>] [--strategy-from-repo]",
	Short: "Run benchmark",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if *ecosystem == "" || *pkg == "" || *version == "" {
			log.Fatal("ecosystem, package, and version must be provided")
		}
		mode := schema.ExecutionMode(args[0])
		if mode != schema.SmoketestMode && mode != schema.AttestMode {
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
				if mode == schema.AttestMode {
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
			if mode == schema.SmoketestMode {
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
				verdicts = resp.Verdicts
			} else {
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
		switch *format {
		case "", "strategy":
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(resp); err != nil {
				log.Fatal(errors.Wrap(err, "encoding result"))
			}
		case "dockerfile":
			t := rebuild.Target{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Version:   *version,
				Artifact:  *artifact,
			}
			s, err := resp.Strategy()
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing strategy"))
			}
			if s == nil {
				log.Fatal("no strategy")
			}
			in := rebuild.Input{Target: t, Strategy: s}
			dockerfile, err := rebuild.MakeDockerfile(in, rebuild.RemoteOptions{})
			if err != nil {
				log.Fatal(errors.Wrap(err, "generating dockerfile"))
			}
			cmd.OutOrStdout().Write([]byte(dockerfile))
		case "debug-steps":
			t := rebuild.Target{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Version:   *version,
				Artifact:  *artifact,
			}
			s, err := resp.Strategy()
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing strategy"))
			}
			if s == nil {
				log.Fatal("no strategy")
			}
			in := rebuild.Input{Target: t, Strategy: s}
			dockerfile, err := rebuild.MakeDockerfile(in, rebuild.RemoteOptions{})
			if err != nil {
				log.Fatal(errors.Wrap(err, "generating dockerfile"))
			}
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
			t := rebuild.Target{
				Ecosystem: rebuild.Ecosystem(*ecosystem),
				Package:   *pkg,
				Version:   *version,
				Artifact:  *artifact,
			}
			s, err := resp.Strategy()
			if err != nil {
				log.Fatal(errors.Wrap(err, "parsing strategy"))
			}
			if s == nil {
				log.Fatal("no strategy")
			}
			in := rebuild.Input{Target: t, Strategy: s}
			script, err := makeShellScript(in)
			if err != nil {
				log.Fatal(errors.Wrap(err, "generating shell script"))
			}
			cmd.OutOrStdout().Write([]byte(script))

		default:
			log.Fatalf("Unknown --format type: %s", *format)
		}
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
	logsBucket        = flag.String("logs-bucket", "", "the gcs bucket where gcb logs are stored")
	metadataBucket    = flag.String("metadata-bucket", "", "the gcs bucket where rebuild output is stored")
	useNetworkProxy   = flag.Bool("use-network-proxy", false, "request the newtwork proxy")
	useSyscallMonitor = flag.Bool("use-syscall-monitor", false, "request syscall monitoring")
	// run-bench
	maxConcurrency = flag.Int("max-concurrency", 90, "maximum number of inflight requests")
	buildLocal     = flag.Bool("local", false, "true if this request is going direct to build-local (not through API first)")
	async          = flag.Bool("async", false, "true if this benchmark should run asynchronously")
	taskQueuePath  = flag.String("task-queue", "", "the path identifier of the task queue to use")
	taskQueueEmail = flag.String("task-queue-email", "", "the email address of the serivce account Cloud Tasks should authorize as")
	// run-one
	strategyPath = flag.String("strategy", "", "the strategy file to use")
	// get-results
	runFlag      = flag.String("run", "", "the run(s) from which to fetch results")
	bench        = flag.String("bench", "", "a path to a benchmark file. if provided, only results from that benchmark will be fetched")
	format       = flag.String("format", "", "format of the output, options are command specific")
	assetType    = flag.String("asset-type", "", "the type of asset that should be fetched")
	prefix       = flag.String("prefix", "", "filter results to those matching this prefix ")
	pattern      = flag.String("pattern", "", "filter results to those matching this regex pattern")
	sample       = flag.Int("sample", -1, "if provided, only N results will be displayed")
	project      = flag.String("project", "", "the project from which to fetch the Firestore data")
	clean        = flag.Bool("clean", false, "whether to apply normalization heuristics to group similar verdicts")
	debugStorage = flag.String("debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	// TUI
	benchmarkDir = flag.String("benchmark-dir", "", "a directory with benchmarks to work with")
	defDir       = flag.String("def-dir", "", "tui will make edits to strategies in this manual build definition repo")
	llmProject   = flag.String("llm-project", "", "the GCP project to use for LLM execution")
	// Migrate
	dryrun = flag.Bool("dryrun", false, "true if this migration is a dryrun")
)

func init() {
	runBenchmark.Flags().AddGoFlag(flag.Lookup("api"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("max-concurrency"))
	runBenchmark.Flags().AddGoFlag(flag.Lookup("local"))
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
	getResults.Flags().AddGoFlag(flag.Lookup("asset-type"))
	getResults.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	getResults.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	getResults.Flags().AddGoFlag(flag.Lookup("metadata-bucket"))

	tui.Flags().AddGoFlag(flag.Lookup("project"))
	tui.Flags().AddGoFlag(flag.Lookup("llm-project"))
	tui.Flags().AddGoFlag(flag.Lookup("debug-storage"))
	tui.Flags().AddGoFlag(flag.Lookup("logs-bucket"))
	tui.Flags().AddGoFlag(flag.Lookup("metadata-bucket"))
	tui.Flags().AddGoFlag(flag.Lookup("benchmark-dir"))
	tui.Flags().AddGoFlag(flag.Lookup("clean"))
	tui.Flags().AddGoFlag(flag.Lookup("def-dir"))

	listRuns.Flags().AddGoFlag(flag.Lookup("project"))
	listRuns.Flags().AddGoFlag(flag.Lookup("bench"))

	infer.Flags().AddGoFlag(flag.Lookup("api"))
	infer.Flags().AddGoFlag(flag.Lookup("format"))
	infer.Flags().AddGoFlag(flag.Lookup("ecosystem"))
	infer.Flags().AddGoFlag(flag.Lookup("package"))
	infer.Flags().AddGoFlag(flag.Lookup("version"))
	infer.Flags().AddGoFlag(flag.Lookup("artifact"))

	migrate.Flags().AddGoFlag(flag.Lookup("project"))
	migrate.Flags().AddGoFlag(flag.Lookup("dryrun"))

	rootCmd.AddCommand(runBenchmark)
	rootCmd.AddCommand(runOne)
	rootCmd.AddCommand(getResults)
	rootCmd.AddCommand(tui)
	rootCmd.AddCommand(listRuns)
	rootCmd.AddCommand(infer)
	rootCmd.AddCommand(migrate)
}

func main() {
	flag.Parse()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
