// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/serviceid"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// Link-time configured service identity
var (
	// Repo from which the service was built
	BuildRepo string
	// Golang version identifier of the service container builds
	BuildVersion string
)

func main() {
	ctx := context.Background()

	cfg, err := schema.RebuildDepsConfigFromEnv()
	if err != nil {
		log.Fatalf("failed to load rebuild deps config: %v", err)
	}
	if cfg == nil {
		log.Fatalf("REBUILD_DEPS_CONFIG not set")
	}

	opID := os.Getenv("OP_ID")
	if opID == "" {
		log.Fatalf("OP_ID not set")
	}

	reqJSON := os.Getenv("REBUILD_REQUEST")
	if reqJSON == "" {
		log.Fatalf("REBUILD_REQUEST not set")
	}
	var req schema.RebuildPackageRequest
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		log.Fatalf("failed to unmarshal rebuild request: %v", err)
	}

	// Reconstruct deps for RebuildPackage.
	deps, err := apiservice.MakeRebuildPackageDeps(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to make rebuild package deps: %v", err)
	}
	deps.ServiceRepo, err = serviceid.ParseLocation(BuildRepo, BuildVersion)
	if err != nil {
		log.Fatalf("parsing service location: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, req.BuildTimeout)
	defer cancel()
	if _, err := apiservice.RebuildPackage(ctx, req, deps); err != nil {
		log.Fatalf("Rebuilding package: %v", err)
	}
	// No need to update deps.Attempts, RebuildPackage will handle the terminal state.
}
