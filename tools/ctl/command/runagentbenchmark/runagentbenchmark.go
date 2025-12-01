// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package runagentbenchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/oauth"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	runv2 "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config holds all configuration for the run-agent-bench command.
type Config struct {
	Project         string
	API             string
	MaxConcurrency  int
	AgentIterations int
	BenchmarkFile   string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
	}
	if c.BenchmarkFile == "" {
		return errors.New("benchmark file is required")
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
		return errors.New("expected exactly 1 argument: benchmark file")
	}
	cfg.BenchmarkFile = args[0]
	return nil
}

// Handler contains the business logic for the run-agent-bench command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	runService, err := runv2.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud Run service")
	}

	var agentStub api.StubT[schema.AgentCreateRequest, schema.AgentCreateResponse]
	var runStub api.StubT[schema.CreateRunRequest, schema.Run]
	{
		apiURL, err := url.Parse(cfg.API)
		if err != nil {
			return nil, errors.Wrap(err, "parsing API endpoint")
		}
		var client *http.Client
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
		agentStub = api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent"))
		runStub = api.Stub[schema.CreateRunRequest, schema.Run](client, apiURL.JoinPath("runs"))
	}
	path := cfg.BenchmarkFile
	log.Printf("Extracting benchmark %s...\n", filepath.Base(path))
	set, err := benchmark.ReadBenchmark(path)
	if err != nil {
		return nil, errors.Wrap(err, "reading benchmark file")
	}
	log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	// Create the Run object
	var runID string
	{
		resp, err := runStub(ctx, schema.CreateRunRequest{
			BenchmarkName: filepath.Base(path),
			BenchmarkHash: hex.EncodeToString(set.Hash(sha256.New())),
			Type:          string(schema.AgentMode),
		})
		if err != nil {
			return nil, errors.Wrap(err, "creating run")
		}
		runID = resp.ID
	}
	log.Printf("Beginning run %s", runID)
	p := pipe.Into(pipe.FromSlice(set.Packages), func(in benchmark.Package, out chan<- schema.AgentCreateRequest) {
		if len(in.Versions) > 0 && len(in.Versions) != len(in.Artifacts) {
			log.Printf("Package %s has mismatching version and artifacts", in.Name)
			return
		}
		for i, v := range in.Versions {
			req := schema.AgentCreateRequest{
				RunID: runID,
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(in.Ecosystem),
					Package:   in.Name,
					Version:   v,
				},
				MaxIterations: cfg.AgentIterations,
			}
			if len(in.Artifacts) > 0 {
				req.Target.Artifact = in.Artifacts[i]
			}
			out <- req
		}
	})
	bar := pb.New(set.Count)
	bar.Output = deps.IO.Err
	bar.ShowTimeLeft = true
	bar.Start()
	p2 := pipe.ParInto(cfg.MaxConcurrency, p, func(in schema.AgentCreateRequest, out chan<- string) {
		defer bar.Increment()
		resp, err := agentStub(ctx, in)
		if err != nil {
			log.Println(err)
			return
		}
		sessionDoc := fire.Collection("agent_sessions").Doc(resp.SessionID)
		var session schema.AgentSession
		for { // TODO: Avoid infinite loops with whole-loop timeout
			time.Sleep(30 * time.Second)
			sessionSnap, err := sessionDoc.Get(ctx)
			if err != nil && status.Code(err) == codes.NotFound {
				continue
			} else if err != nil {
				log.Println(errors.Wrap(err, "getting session document"))
				return
			}
			if err := sessionSnap.DataTo(&session); err != nil {
				log.Fatal("deserializing session data")
				return
			}
			if session.Status == schema.AgentSessionStatusCompleted {
				break
			}
			e, err := runService.Projects.Locations.Jobs.Executions.Get(resp.ExeuctionName).Do()
			if err != nil {
				log.Printf("Failed to get execution %s: %v", resp.ExeuctionName, err)
			} else if e.CompletionTime != "" {
				// Execution has terminated, but session not marked as complete.
				// TODO: Clean up the session in this case.
				log.Printf("Job execution %s terminated but session %s not complete. Breaking.", resp.ExeuctionName, resp.SessionID)
				break
			}
		}
		out <- session.StopReason
	})
	var successes, total int
	for reason := range p2.Out() {
		if reason == schema.AgentCompleteReasonSuccess {
			successes++
		}
		total++
	}
	bar.Finish()
	log.Printf("Successes: %d/%d\n", successes, total)
	return &act.NoOutput{}, nil
}

// Command creates a new run-agent-bench command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "run-agent-bench --project <project> --api <URI> [--max-concurrency <concurrency>] [--agent-iterations <max iterations>] <benchmark.json>",
		Short: "Run benchmark on the agent",
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
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.IntVar(&cfg.MaxConcurrency, "max-concurrency", 90, "maximum number of inflight requests")
	set.IntVar(&cfg.AgentIterations, "agent-iterations", 3, "maximum number of agent iterations before giving up")
	return set
}
