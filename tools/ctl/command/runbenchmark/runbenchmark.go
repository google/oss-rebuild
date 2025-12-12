// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runbenchmark

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	benchrun "github.com/google/oss-rebuild/tools/benchmark/run"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the run-bench command.
type Config struct {
	API               string
	Local             bool
	BenchmarkPath     string
	BootstrapBucket   string
	BootstrapVersion  string
	ExecutionMode     schema.ExecutionMode
	Format            string
	Verbose           bool
	Async             bool
	TaskQueue         string
	TaskQueueEmail    string
	UseNetworkProxy   bool
	UseSyscallMonitor bool
	OverwriteMode     string
	MaxConcurrency    int
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if !c.Local && c.API == "" {
		return errors.New("API is required when not running locally")
	}
	if c.Local && (c.BootstrapBucket == "" || c.BootstrapVersion == "") {
		return errors.New("bootstrap-bucket and bootstrap-version are required when running locally")
	}
	if c.Format != "" && c.Format != "summary" && c.Format != "csv" {
		return errors.Errorf("invalid format: %s. Expected one of 'summary' or 'csv'", c.Format)
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

func isCloudRun(u *url.URL) bool {
	return strings.HasSuffix(u.Host, ".run.app")
}

func parseArgs(cfg *Config, args []string) error {
	if len(args) != 2 {
		return errors.New("expected exactly 2 arguments")
	}
	mode := schema.ExecutionMode(args[0])
	if mode != schema.SmoketestMode && mode != schema.AttestMode {
		return errors.Errorf("Unknown mode: %s. Expected one of 'smoketest' or 'attest'", string(mode))
	}
	cfg.ExecutionMode = mode
	cfg.BenchmarkPath = args[1]
	if cfg.BenchmarkPath == "" {
		return errors.Errorf("benchmark path is required")
	}
	return nil
}

// Handler contains the business logic for the run-bench command.
// This function does not depend on Cobra and can be tested independently.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	var set benchmark.PackageSet
	var err error
	{
		log.Printf("Extracting benchmark %s...\n", filepath.Base(cfg.BenchmarkPath))
		set, err = benchmark.ReadBenchmark(cfg.BenchmarkPath)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	var apiURL *url.URL
	var runID string
	var dex rundex.Writer
	var executor benchrun.ExecutionService
	if cfg.Local {
		now := time.Now().UTC()
		runID = now.Format(time.RFC3339)
		store, err := localfiles.AssetStore(runID)
		if err != nil {
			return nil, errors.Wrap(err, "Failed to create temp directory")
		}
		// TODO: Validate this.
		prebuildURL := fmt.Sprintf("https://%s.storage.googleapis.com/%s", cfg.BootstrapBucket, cfg.BootstrapVersion)
		executor = benchrun.NewLocalExecutionService(prebuildURL, store, deps.IO.Out)
		dex = rundex.NewFilesystemClient(localfiles.Rundex())
		if err := dex.WriteRun(ctx, rundex.FromRun(schema.Run{
			ID:            runID,
			BenchmarkName: filepath.Base(cfg.BenchmarkPath),
			BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
			Type:          string(schema.SmoketestMode),
			Created:       now,
		})); err != nil {
			log.Println(errors.Wrap(err, "writing run to rundex"))
		}
	} else {
		if cfg.API == "" {
			return nil, errors.New("API endpoint not provided")
		}
		apiURL, err = url.Parse(cfg.API)
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
		executor = benchrun.NewRemoteExecutionService(client, apiURL)
		stub := api.Stub[schema.CreateRunRequest, schema.Run](client, apiURL.JoinPath("runs"))
		resp, err := stub(ctx, schema.CreateRunRequest{
			BenchmarkName: filepath.Base(cfg.BenchmarkPath),
			BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
			Type:          string(cfg.ExecutionMode),
		})
		if err != nil {
			return nil, errors.Wrap(err, "creating run")
		}
		runID = resp.ID
	}
	if cfg.Async {
		if cfg.Local {
			return nil, errors.New("Unsupported async local execution")
		}
		queue, err := taskqueue.NewQueue(ctx, cfg.TaskQueue, cfg.TaskQueueEmail)
		if err != nil {
			return nil, errors.Wrap(err, "making taskqueue client")
		}
		if err := benchrun.RunBenchAsync(ctx, set, cfg.ExecutionMode, apiURL, runID, queue); err != nil {
			return nil, errors.Wrap(err, "adding benchmark to queue")
		}
		return &act.NoOutput{}, nil
	}
	bar := pb.New(set.Count)
	bar.Output = deps.IO.Err
	bar.ShowTimeLeft = true
	verdictChan, err := benchrun.RunBench(ctx, set, benchrun.RunBenchOpts{
		ExecService:       executor,
		Mode:              cfg.ExecutionMode,
		RunID:             runID,
		MaxConcurrency:    cfg.MaxConcurrency,
		UseSyscallMonitor: cfg.UseSyscallMonitor,
		UseNetworkProxy:   cfg.UseNetworkProxy,
		OverwriteMode:     schema.OverwriteMode(cfg.OverwriteMode),
	})
	if err != nil {
		return nil, errors.Wrap(err, "running benchmark")
	}
	var verdicts []schema.Verdict
	bar.Start()
	for v := range verdictChan {
		bar.Increment()
		if cfg.Verbose && v.Message != "" {
			fmt.Fprintf(deps.IO.Out, "\n%v: %s\n", v.Target, v.Message)
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
	switch cfg.Format {
	// TODO: Maybe add more format options, or include more data in the csv?
	case "", "summary":
		var successes int
		for _, v := range verdicts {
			if v.Message == "" {
				successes++
			}
		}
		io.WriteString(deps.IO.Out, fmt.Sprintf("Successes: %d/%d\n", successes, len(verdicts)))
	case "csv":
		w := csv.NewWriter(deps.IO.Out)
		defer w.Flush()
		for _, v := range verdicts {
			if err := w.Write([]string{fmt.Sprintf("%v", v.Target), v.Message}); err != nil {
				return nil, errors.Wrap(err, "writing CSV")
			}
		}
	default:
		return nil, errors.Errorf("Unsupported format: %s", cfg.Format)
	}
	return &act.NoOutput{}, nil
}

// Command creates a new run-bench command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "run-bench smoketest|attest -api <URI>  [-local -bootstrap-bucket <BUCKET> -bootstrap-version <VERSION>] [-format=summary|csv] <benchmark.json>",
		Short: "Run benchmark",
		Args:  cobra.ExactArgs(2),
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
	set.IntVar(&cfg.MaxConcurrency, "max-concurrency", 90, "maximum number of inflight requests")
	set.BoolVar(&cfg.Local, "local", false, "true if this request is going direct to build-local (not through API first)")
	set.StringVar(&cfg.BootstrapBucket, "bootstrap-bucket", "", "the gcs bucket where bootstrap tools are stored")
	set.StringVar(&cfg.BootstrapVersion, "bootstrap-version", "", "the version of bootstrap tools to use")
	set.StringVar(&cfg.Format, "format", "", "format of the output (summary|csv)")
	set.BoolVar(&cfg.Verbose, "v", false, "verbose output")
	set.BoolVar(&cfg.Async, "async", false, "true if this benchmark should run asynchronously")
	set.StringVar(&cfg.TaskQueue, "task-queue", "", "the path identifier of the task queue to use")
	set.StringVar(&cfg.TaskQueueEmail, "task-queue-email", "", "the email address of the serivce account Cloud Tasks should authorize as")
	set.BoolVar(&cfg.UseNetworkProxy, "use-network-proxy", false, "request the newtwork proxy")
	set.BoolVar(&cfg.UseSyscallMonitor, "use-syscall-monitor", false, "request syscall monitoring")
	set.StringVar(&cfg.OverwriteMode, "overwrite-mode", "", "reason to overwrite existing attestation (SERVICE_UPDATE or FORCE)")
	return set
}
