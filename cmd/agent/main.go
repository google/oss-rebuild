// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/agent"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
	"google.golang.org/genai"
)

var (
	project         = flag.String("project", "", "GCP Project ID for resource usage")
	location        = flag.String("location", "global", "GCP location for resource usage")
	model           = flag.String("model", "", "Gemini model id for the session, if overriding defaults")
	sessionID       = flag.String("session-id", "", "Session ID for this agent run")
	agentAPIURL     = flag.String("agent-api-url", "", "URL of the agent API service")
	gitCacheURL     = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
	sessionsBucket  = flag.String("sessions-bucket", "", "GCS bucket for session data")
	metadataBucket  = flag.String("metadata-bucket", "", "GCS bucket for build metadata")
	logsBucket      = flag.String("logs-bucket", "", "GCS bucket for build logs")
	maxIterations   = flag.Int("max-iterations", 20, "Maximum number of iterations")
	targetEcosystem = flag.String("target-ecosystem", "", "Target package ecosystem")
	targetPackage   = flag.String("target-package", "", "Target package name")
	targetVersion   = flag.String("target-version", "", "Target package version")
	targetArtifact  = flag.String("target-artifact", "", "Target package artifact")
	// Scratch execution mode flags. The scratch VM is allocated by the
	// session creator. The agent only receives its handle.
	executionMode  = flag.String("execution-mode", string(schema.AgentExecutionModeGCB), "Where iteration builds execute: gcb or scratch")
	scratchID      = flag.String("scratch-id", "", "Handle of the scratch VM allocated for this session (required for scratch execution mode)")
	buildTimeout   = flag.Duration("build-timeout", time.Hour, "Per-iteration build timeout for scratch execution")
	prebuildBucket = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	prebuildDir    = flag.String("prebuild-dir", "", "Prefix within the prebuild bucket under which tools are stored")
	prebuildAuth   = flag.Bool("prebuild-auth", false, "Whether to authenticate requests to the prebuild tools bucket")
)

var httpcfg = httpegress.Config{}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if *project == "" {
		log.Fatal("project flag is required")
	}
	if *sessionID == "" {
		log.Fatal("session-id flag is required")
	}
	if *agentAPIURL == "" {
		log.Fatal("agent-api-url flag is required")
	}
	if *sessionsBucket == "" {
		log.Fatal("sessions-bucket flag is required")
	}
	// Both modes read these buckets: GCB mode for every iteration, scratch
	// mode for its GCB confirmation builds' metadata and logs.
	if *metadataBucket == "" {
		log.Fatal("metadata-bucket flag is required")
	}
	if *logsBucket == "" {
		log.Fatal("logs-bucket flag is required")
	}
	mode := schema.AgentExecutionMode(*executionMode)
	switch mode {
	case schema.AgentExecutionModeGCB:
	case schema.AgentExecutionModeScratch:
		if *scratchID == "" {
			log.Fatal("scratch-id flag is required for scratch execution mode")
		}
	default:
		log.Fatalf("invalid execution-mode %q", *executionMode)
	}
	if *targetEcosystem == "" {
		log.Fatal("target-ecosystem flag is required")
	}
	if *targetPackage == "" {
		log.Fatal("target-package flag is required")
	}
	if *targetVersion == "" {
		log.Fatal("target-version flag is required")
	}
	if *targetArtifact == "" {
		log.Fatal("target-artifact flag is required")
	}
	if *maxIterations <= 0 {
		log.Fatal("max-iterations flag must be positive")
	}
	ctx := context.Background()
	// Create HTTP client for the agent API
	client, err := idtoken.NewClient(ctx, *agentAPIURL)
	if err != nil {
		log.Fatalf("Failed to create API client: %v", err)
	}
	baseURL, err := url.Parse(*agentAPIURL)
	if err != nil {
		log.Fatalf("Failed to parse agent API URL: %v", err)
	}
	// Create agent API client stubs
	iterationStub := api.Stub[schema.AgentCreateIterationRequest, schema.AgentCreateIterationResponse](client, baseURL.JoinPath("agent/session/iteration"))
	completeStub := api.Stub[schema.AgentCompleteRequest, schema.AgentCompleteResponse](client, baseURL.JoinPath("agent/session/complete"))
	aiClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  *project,
		Location: *location,
	})
	if err != nil {
		log.Fatal("Failed to create genai client: ", err)
	}
	gcsClient, err := gcs.NewClient(ctx)
	if err != nil {
		log.Fatal("Failed to create GCS client: ", err)
	}
	regclient, err := httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		log.Fatal("Failed to create egress client: ", err)
	}
	var gitCache *gitcache.Client
	if *gitCacheURL != "" {
		idc, err := idtoken.NewClient(ctx, *gitCacheURL)
		if err != nil {
			log.Fatal("Failed to create git cache id client: ", err)
		}
		apic, _, err := gapihttp.NewClient(ctx)
		if err != nil {
			log.Fatal("Failed to create git cache API client: ", err)
		}
		u, err := url.Parse(*gitCacheURL)
		if err != nil {
			log.Fatal("Failed to parse git cache URL: ", err)
		}
		gitCache = &gitcache.Client{IDClient: idc, APIClient: apic, URL: u}
	}
	target := rebuild.Target{Ecosystem: rebuild.Ecosystem(*targetEcosystem), Package: *targetPackage, Version: *targetVersion, Artifact: *targetArtifact}
	deps := agent.RunSessionDeps{
		Client:         aiClient,
		IterationStub:  iterationStub,
		CompleteStub:   completeStub,
		GCSClient:      gcsClient,
		SessionsBucket: *sessionsBucket,
		MetadataBucket: *metadataBucket,
		LogsBucket:     *logsBucket,
		RegistryClient: regclient,
		Retrier:        agent.NewRetrier(),
		Model:          *model,
		GitCache:       gitCache,
	}
	if mode == schema.AgentExecutionModeScratch {
		stubs := scratch.Stubs{
			ExecCreate: api.Stub[schema.ScratchExecRequest, longrunning.Operation[schema.ScratchExecResult]](client, baseURL.JoinPath("scratch/exec/op/create")),
			ExecGet:    api.Stub[schema.GetOperationRequest, longrunning.Operation[schema.ScratchExecResult]](client, baseURL.JoinPath("scratch/exec/op/get")),
		}
		var authHeader func(context.Context) (string, error)
		if *prebuildAuth {
			ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/devstorage.read_only")
			if err != nil {
				log.Fatal("Failed to create prebuild token source: ", err)
			}
			authHeader = func(context.Context) (string, error) {
				t, err := ts.Token()
				if err != nil {
					return "", errors.Wrap(err, "minting prebuild token")
				}
				return "Authorization: Bearer " + t.AccessToken, nil
			}
		}
		executor, err := scratch.NewDockerRunExecutor(scratch.DockerRunExecutorConfig{
			ExecutorConfig: scratch.ExecutorConfig{
				ScratchID:      *scratchID,
				Stubs:          stubs,
				GCSClient:      gcsClient,
				DefaultTimeout: *buildTimeout,
				AuthHeader:     authHeader,
				// NOTE: The scratch VM is dedicated to this session's builds, so
				// privileged plans are acceptable there.
				AllowPrivileged: true,
				// Keep the most recent build's container (stopped, not --rm'd) so
				// run_command can docker exec/cp into the build environment.
				RetainContainer: true,
			},
		})
		if err != nil {
			log.Fatal("Failed to create scratch executor: ", err)
		}
		deps.ScratchRunner = &agent.ScratchRunner{
			Target:         target,
			Executor:       executor,
			ScratchID:      *scratchID,
			Stubs:          stubs,
			GCSClient:      gcsClient,
			RegistryClient: regclient,
			PrebuildConfig: rebuild.PrebuildConfig{Bucket: *prebuildBucket, Dir: *prebuildDir, Auth: *prebuildAuth},
			BuildTimeout:   *buildTimeout,
		}
	}
	req := agent.RunSessionReq{
		SessionID:     *sessionID,
		Target:        target,
		MaxIterations: *maxIterations,
	}
	log.Printf("Agent running for session %s (execution mode %s), target: %+v", req.SessionID, mode, req.Target)
	agent.RunSession(ctx, req, deps)
}
