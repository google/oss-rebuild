// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/url"

	"github.com/google/oss-rebuild/internal/api"
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

	// Create agent API client stub
	completeStub := api.Stub[schema.AgentCompleteRequest, any](client, baseURL.JoinPath("agent/session/complete"))

	// Run the agent as a no-op but call Complete to properly work with the system
	log.Printf("Agent running for session %s", *sessionID)
	log.Printf("Target: ecosystem=%s package=%s version=%s artifact=%s", *targetEcosystem, *targetPackage, *targetVersion, *targetArtifact)

	// TODO: Implement actual agent logic here
	// TODO: Make sure complete gets invoked on any failure!
	// For now, just complete immediately as a no-op
	req := schema.AgentCompleteRequest{
		SessionID:  *sessionID,
		StopReason: "NO_OP",
		Summary:    "Agent completed as no-op stub",
	}

	_, err = completeStub(ctx, req)
	if err != nil {
		log.Fatalf("Failed to complete agent session: %v", err)
	}

	log.Printf("Agent session %s completed successfully", *sessionID)
}
