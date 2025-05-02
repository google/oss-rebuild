// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/vertexai/genai"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/llm"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/debian"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
)

var (
	apiURI       = flag.String("api", "", "OSS Rebuild API endpoint URI")
	project      = flag.String("project", "", "the project from which to fetch the Firestore data")
	runFlag      = flag.String("run", "", "the run from which to originate failures")
	debugStorage = flag.String("debug-storage", "", "the gcs bucket to find debug logs and artifacts")
	baseModel    = flag.String("model", "", fmt.Sprintf("Base model to use (options: %s, %s)", llm.GeminiPro, llm.GeminiFlash))
	modelProject = flag.String("model-project", "", "the project from which to fetch and run model inference")
	requireAck   = flag.Bool("require-ack", true, "if true, prompt the user for each build that would be executed")
	dryRun       = flag.Bool("dry-run", true, "if true, only print what would be executed")
)

const (
	AttemptAsset       rebuild.AssetType = "attempt.json"
	SourceArchiveAsset rebuild.AssetType = "src.zip"
)

type OutcomeType int

const (
	OutcomeUnknown OutcomeType = iota
	OutcomeSuccess
	OutcomeFailedInference
	OutcomeFailedFetchDeps
	OutcomeFailedRunBuild
	OutcomeCompareMismatch
)

func (t OutcomeType) String() string {
	return map[OutcomeType]string{
		OutcomeSuccess:         "Success",
		OutcomeFailedInference: "Infer Failure",
		OutcomeFailedFetchDeps: "Deps Failure",
		OutcomeFailedRunBuild:  "Build Failure",
		OutcomeCompareMismatch: "Compare Mismatch",
		OutcomeUnknown:         "Unknown Outcome",
	}[t]
}

func hasAnyPrefix(s string, pats []string) bool {
	for _, pat := range pats {
		if strings.HasPrefix(s, pat) {
			return true
		}
	}
	return false
}

// classifyOutcome determines the type of failure that occurred
func classifyOutcome(r rundex.Rebuild) OutcomeType {
	if _, err := r.Strategy.Strategy(); err != nil {
		return OutcomeFailedInference
	}
	if r.Message == "" {
		return OutcomeSuccess
	}
	if strings.HasPrefix(r.Message, "failed to execute strategy.Deps") {
		return OutcomeFailedFetchDeps
	} else if hasAnyPrefix(r.Message, []string{
		"Excess CRLF",
		"missing build tool",
		"file(s) found in",
		"dist/ file(s)",
		"hidden file(s)",
		"content differences",
	}) {
		return OutcomeCompareMismatch
	}
	return OutcomeFailedRunBuild
}

func location(s rebuild.Strategy) *rebuild.Location {
	switch t := s.(type) {
	case *rebuild.LocationHint:
		return &t.Location
	case *pypi.PureWheelBuild:
		return &t.Location
	case *npm.NPMPackBuild:
		return &t.Location
	case *npm.NPMCustomBuild:
		return &t.Location
	case *cratesio.CratesIOCargoPackage:
		return &t.Location
	case *rebuild.ManualStrategy:
		return &t.Location
	case *debian.DebianPackage:
		return nil
	default:
		return nil
	}
}

// fromSourceZip extracts and returns the content of "path" inside the SourceArchiveAsset zip for target "t".
func fromSourceZip(ctx context.Context, ls rebuild.AssetStore, t rebuild.Target, path string) (string, error) {
	r, err := ls.Reader(ctx, rebuild.Asset{Target: t, Type: SourceArchiveAsset})
	if err != nil {
		return "", errors.Wrap(err, "getting asset reader")
	}
	bts, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "reading asset contents")
	}
	br := bytes.NewReader(bts)
	zr, err := zip.NewReader(br, int64(len(bts)))
	if err != nil {
		return "", errors.Wrap(err, "creating zip reader")
	}
	// NOTE: Search within the top-level directory, whatever its name.
	basename := filepath.SplitList(zr.File[0].Name)[0]
	fullpath := filepath.Join(basename, path)
	f, err := zr.Open(fullpath)
	if err != nil {
		return "", errors.Wrapf(err, "opening %q from zip", fullpath)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", errors.Wrap(err, "reading file contents")
	}
	return string(b), nil
}

func generateRecovery(ctx context.Context, model *genai.GenerativeModel, cache rebuild.AssetStore, r rundex.Rebuild, commands []string) (*llm.ScriptResponse, error) {
	outcome := classifyOutcome(r)
	s, err := r.Strategy.Strategy()
	if err != nil {
		return nil, errors.New("no strategy for rebuild")
	}
	switch outcome {
	case OutcomeCompareMismatch:
		packageJSON, err := fromSourceZip(ctx, cache, r.Target(), filepath.Join(location(s).Dir, "package.json"))
		if err != nil {
			return nil, errors.Wrap(err, "reading package.json")
		}
		return llm.InferNPMBuild(ctx, *model, packageJSON)
	case OutcomeFailedRunBuild:
		packageJSON, err := fromSourceZip(ctx, cache, r.Target(), filepath.Join(location(s).Dir, "package.json"))
		if err != nil {
			return nil, errors.Wrap(err, "reading package.json")
		}
		reader, err := cache.Reader(ctx, rebuild.Asset{Target: r.Target(), Type: rebuild.DebugLogsAsset})
		if err != nil {
			return nil, errors.Wrap(err, "creating build log reader")
		}
		logBytes, err := io.ReadAll(reader)
		if err != nil {
			return nil, errors.Wrap(err, "reading build log")
		}
		buildLog := string(logBytes)
		return llm.FixNPMBreakage(ctx, *model, strings.Join(commands, "\n"), packageJSON, buildLog)
	default:
		return nil, errors.Errorf("unhandled outcome '%s'", outcome)
	}
}

func validateFlags() error {
	if *project == "" || *modelProject == "" || *baseModel == "" || *apiURI == "" || *debugStorage == "" || *runFlag == "" {
		return fmt.Errorf("all flags except -dry-run are required")
	}
	switch *baseModel {
	case "pro":
		*baseModel = llm.GeminiPro
	case "flash":
		*baseModel = llm.GeminiFlash
	case llm.GeminiPro, llm.GeminiFlash:
	default:
		return fmt.Errorf("model must be one of: %s, %s", llm.GeminiPro, llm.GeminiFlash)
	}
	return nil
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	flag.Parse()
	if err := validateFlags(); err != nil {
		log.Printf("Parsing flags: %v", err)
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	// Initialize dependencies
	client := must(genai.NewClient(ctx, *modelProject, "us-central1"))
	model := client.GenerativeModel(*baseModel)
	model = llm.WithSystemPrompt(*model, llm.NPMSystemPrompt)
	apiURL := must(url.Parse(*apiURI))
	idclient := must(oauth.AuthorizedUserIDClient(ctx))
	dex := must(rundex.NewFirestore(ctx, *project))
	debug := must(rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, *runFlag), *debugStorage))
	cache := rebuild.NewFilesystemAssetStore(osfs.New("/tmp/eval-cache"))

	// Validate run
	runs := must(dex.FetchRuns(ctx, rundex.FetchRunsOpts{IDs: []string{*runFlag}}))
	if len(runs) != 1 {
		log.Fatalf("Unexpected runs matching '%s': Got %d, Expected 1", *runFlag, len(runs))
	}
	run := runs[0]

	// Create new run ID for each new bulk execution.
	var runID string
	if !*dryRun {
		log.Println("Creating run id...")
		createRun := api.StubFromHandler(idclient, apiURL.JoinPath("runs"), apiservice.CreateRun)
		res := must(createRun(ctx, schema.CreateRunRequest{
			BenchmarkName: run.BenchmarkName,
			BenchmarkHash: run.BenchmarkHash,
			Type:          string(schema.SmoketestMode),
		}))
		runID = res.ID
		log.Printf("Created run id: %s", runID)
	}

	// Query rebuilds
	rebuilds := must(dex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{Runs: []string{run.ID}, LatestPerPackage: true}))
	c := make(chan rundex.Rebuild, 50)
	go func() {
		defer close(c)
		for _, r := range rebuilds {
			c <- r
		}
	}()
	p := pipe.From(c)

	// Cache rebuild metadata
	httpClient := httpx.RateLimitedClient{BasicClient: http.DefaultClient, Ticker: time.NewTicker(time.Second)}
	// NOTE: Not critical but 30 concurrent workers helps lighten load on HTTP client which only allows 1 QPS.
	p = p.ParDo(30, func(r rundex.Rebuild, out chan<- rundex.Rebuild) {
		// Serialize and store schema.SmoketestAttempt.
		if _, err := cache.Reader(ctx, rebuild.Asset{Target: r.Target(), Type: AttemptAsset}); errors.Is(err, fs.ErrNotExist) {
			enc := json.NewEncoder(must(cache.Writer(ctx, rebuild.Asset{Target: r.Target(), Type: AttemptAsset})))
			orDie(enc.Encode(r.RebuildAttempt))
		}
		// Download and store source archive.
		if _, err := cache.Reader(ctx, rebuild.Asset{Target: r.Target(), Type: SourceArchiveAsset}); errors.Is(err, fs.ErrNotExist) {
			s, err := r.Strategy.Strategy()
			if err != nil {
				log.Printf("Skipping %s: %v", r.ID(), errors.Wrap(err, "reading strategy"))
				return
			}
			loc := location(s)
			if loc == nil || loc.Repo == "" || loc.Ref == "" {
				log.Printf("Skipping %s: %v", r.ID(), errors.New("no source ref"))
				return
			}
			// TODO: clone and store the full repo -> enables eval of source recovery algos
			resp := must(httpClient.Do(must(http.NewRequestWithContext(ctx, "GET", loc.Repo+"/archive/"+loc.Ref[:7]+".zip", nil))))
			if resp.StatusCode != 200 {
				log.Printf("Skipping %s: %v", r.ID(), errors.Errorf("fetching source archive: HTTP %d", resp.StatusCode))
				return
			}
			must(io.Copy(must(cache.Writer(ctx, rebuild.Asset{Target: r.Target(), Type: SourceArchiveAsset})), resp.Body))
		}
		// Download and store build logs.
		if _, err := cache.Reader(ctx, rebuild.Asset{Target: r.Target(), Type: rebuild.DebugLogsAsset}); errors.Is(err, fs.ErrNotExist) {
			orDie(rebuild.AssetCopy(ctx, cache, debug, rebuild.Asset{Target: r.Target(), Type: rebuild.DebugLogsAsset}))
		}
		out <- r
	})

	// Perform recovery inference
	type Recovery struct {
		rundex.Rebuild
		OriginalCommands []string
		NewScript        llm.ScriptResponse
		NewStrategy      rebuild.Strategy
	}
	var concurrency int
	switch *baseModel {
	case llm.GeminiFlash:
		concurrency = 10 // Quota: 200 Q/m
	case llm.GeminiPro:
		concurrency = 5 // Quota: 60 Q/m
	default:
		concurrency = 1
	}
	sp := pipe.ParInto(concurrency, p, func(r rundex.Rebuild, out chan<- Recovery) {
		s := must(r.Strategy.Strategy())
		var commands []string
		if _, ok := s.(*npm.NPMPackBuild); ok {
			commands = []string{"npm pack"}
		} else if ns, ok := s.(*npm.NPMCustomBuild); ok {
			commands = []string{"npm install --force", "npm run " + ns.Command, "rm -rf node_modules", "npm pack"}
		}
		candidate, err := generateRecovery(ctx, model, cache, r, commands)
		if err != nil {
			log.Printf("Skipping %s: %v", r.ID(), errors.Wrap(err, "generating candidate"))
			return
		}
		if slices.Equal(commands, candidate.Commands) {
			log.Printf("Skipping %v: Build script remained unchanged", r.ID())
			return
		}
		inst := must(s.GenerateFor(r.Target(), rebuild.BuildEnv{TimewarpHost: "localhost:8081"}))
		for _, cmd := range candidate.Commands {
			if strings.ContainsRune(cmd, '\'') {
				log.Printf("Skipping %v: Build script command contained single quote [%s]", r.ID(), cmd)
				return
			}
		}
		reg := npmreg.HTTPRegistry{Client: http.DefaultClient}
		vmeta := must(reg.Version(ctx, r.Package, r.Version))
		nodeVersion, err := npm.PickNodeVersion(vmeta)
		if err != nil {
			log.Printf("Skipping %v: %v", r.ID(), err)
			return
		}
		npmv, err := npm.PickNPMVersion(vmeta)
		if err != nil {
			log.Printf("Skipping %v: %v", r.ID(), err)
			return
		}
		var newCommands []string
		if strings.HasPrefix(npmv, "6") {
			newCommands = append(newCommands, "npm config set unsafe-perm true")
		}
		{
			// Configure timewarp registry as build-time command because...
			// - Must be set during build phase since recovery often calls `npm install`
			// - Can't be set in deps phase since env may not be shared with build
			// - Can't be set before npx command since the fetched npm version may have been adjusted to one published after the package.
			env := rebuild.BuildEnv{TimewarpHost: "localhost:8081"}
			regURL := must(env.TimewarpURL("npm", must(reg.Package(ctx, r.Package)).UploadTimes[r.Version]))
			newCommands = append(newCommands, "export NPM_CONFIG_REGISTRY="+regURL)
		}
		newCommands = append(newCommands, candidate.Commands...)
		strategy := rebuild.WorkflowStrategy{
			Location: inst.Location,
			Source: []flow.Step{{
				Uses: "git-checkout",
			}},
			Deps: []flow.Step{{
				Uses: "npm/install-node",
				With: map[string]string{"nodeVersion": nodeVersion},
			}},
			Build: []flow.Step{{
				Uses: "npm/npx",
				With: map[string]string{
					"command":    strings.Join(newCommands, " && "),
					"npmVersion": npmv,
					"dir":        "{{.Location.Dir}}",
					"locator":    "/usr/local/bin/",
				},
			}},
			OutputDir: inst.Location.Dir,
		}
		out <- Recovery{
			Rebuild:          r,
			OriginalCommands: commands,
			NewScript:        *candidate,
			NewStrategy:      &strategy,
		}
	})

	// Solicit human ack before rebuilding, if required
	if *requireAck {
		sp = sp.Do(func(rec Recovery, out chan<- Recovery) {
			log.Printf("Prompting for user ack\n Package: %s\n Version: %s\n Orig: %s\n New: %s\n\n Reason: %s", rec.Rebuild.Package, rec.Rebuild.Version, strings.Join(rec.OriginalCommands, "; "), strings.Join(rec.NewScript.Commands, "; "), rec.NewScript.Reason)
			log.Printf("Proceed with build? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, err := reader.ReadString('\n')
			if err != nil {
				log.Printf("Skipping %v: error reading user input: %v", rec.Rebuild.ID(), err)
				return
			}
			response = strings.ToLower(strings.TrimSpace(response))
			if response == "y" || response == "yes" {
				out <- rec
			} else {
				log.Printf("Skipping %v: user skipped", rec.Rebuild.ID())
			}
		})
	}

	// Evaluate recoveries
	rebuildSmoketest := api.StubFromHandler(idclient, apiURL.JoinPath("smoketest"), apiservice.RebuildSmoketest)
	sp = sp.ParDo(50, func(rec Recovery, _ chan<- Recovery) {
		if *dryRun {
			log.Printf("Dry run would execute build for %s: %s", rec.Rebuild.ID(), strings.Join(rec.NewScript.Commands, "; "))
			return
		}
		log.Printf("Running rebuild for %s: %s", rec.Rebuild.ID(), strings.Join(rec.NewScript.Commands, "; "))
		t := rec.Rebuild.Target()
		oneof := schema.NewStrategyOneOf(rec.NewStrategy)
		resp, err := rebuildSmoketest(ctx, schema.SmoketestRequest{
			Ecosystem: t.Ecosystem,
			Package:   t.Package,
			Versions:  []string{t.Version},
			Strategy:  &oneof,
			ID:        runID,
		})
		if err != nil {
			log.Printf("Failed to rebuild %s: %v", rec.Rebuild.ID(), err)
		} else {
			log.Printf("Rebuilt %s: %v", rec.Rebuild.ID(), resp.Verdicts[0].Message)
		}
	})

	// Block until closed
	for range sp.Out() {
	}
}
