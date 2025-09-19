// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/url"

	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"github.com/google/oss-rebuild/internal/agent"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/idtoken"
)

var (
	sessionID       = flag.String("session-id", "", "Session ID for this agent run")
	agentAPIURL     = flag.String("agent-api-url", "", "URL of the agent API service")
	sessionsBucket  = flag.String("sessions-bucket", "", "GCS bucket for session data")
	metadataBucket  = flag.String("metadata-bucket", "", "GCS bucket for build metadata")
	maxIterations   = flag.Int("max-iterations", 20, "Maximum number of iterations")
	targetEcosystem = flag.String("target-ecosystem", "", "Target package ecosystem")
	targetPackage   = flag.String("target-package", "", "Target package name")
	targetVersion   = flag.String("target-version", "", "Target package version")
	targetArtifact  = flag.String("target-artifact", "", "Target package artifact")
)

func main() {
	flag.Parse()
	if *sessionID == "" {
		log.Fatal("session-id flag is required")
	}
	if *agentAPIURL == "" {
		log.Fatal("agent-api-url flag is required")
	}
	if *sessionsBucket == "" {
		log.Fatal("sessions-bucket flag is required")
	}
	if *metadataBucket == "" {
		log.Fatal("metadata-bucket flag is required")
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
	// Create HTTP client and API URL
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
	g := genkit.Init(ctx, genkit.WithPlugins(&googlegenai.VertexAI{}), genkit.WithDefaultModel("vertexai/gemini-2.5-pro"))
	deps := agent.RunSessionDeps{
		Genkit:         g,
		IterationStub:  iterationStub,
		CompleteStub:   completeStub,
		SessionsBucket: *sessionsBucket,
		MetadataBucket: *metadataBucket,
	}
	req := agent.RunSessionReq{
		SessionID:     *sessionID,
		Target:        rebuild.Target{Ecosystem: rebuild.Ecosystem(*targetEcosystem), Package: *targetPackage, Version: *targetVersion, Artifact: *targetArtifact},
		MaxIterations: *maxIterations,
	}
	log.Printf("Agent running for session %s, target: %+v", req.SessionID, req.Target)
	agent.RunSession(ctx, req, deps)
}
