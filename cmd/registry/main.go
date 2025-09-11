// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
)

var (
	cacheDir                  = flag.String("cache-dir", "/tmp/registry-cache", "Directory to cache registry indices")
	maxSnapshots              = flag.Int("max-snapshots", 4, "Maximum number of snapshot indices to cache")
	currentUpdateIntervalMins = flag.Int("current-update-interval-mins", 30, "Update interval for current index in minutes")
)

var indexManager *index.IndexManager

func init() {
	// Ensure cache directory exists
	err := os.MkdirAll(*cacheDir, 0755)
	if err != nil {
		log.Fatal(errors.Wrap(err, "failed to create cache directory"))
	}
	// Create filesystem for cache directory
	fs := osfs.New(*cacheDir)
	cfg := index.IndexManagerConfig{
		Filesystem:            fs,
		MaxSnapshots:          *maxSnapshots,
		CurrentUpdateInterval: time.Duration(*currentUpdateIntervalMins) * time.Minute,
	}
	indexManager, err = index.NewIndexManagerFromFS(cfg)
	if err != nil {
		log.Fatal(errors.Wrap(err, "failed to create index manager"))
	}
}

func FindRegistryCommitInit(ctx context.Context) (*schema.FindRegistryCommitDeps, error) {
	return &schema.FindRegistryCommitDeps{IndexManager: indexManager}, nil
}

func main() {
	flag.Parse()
	// Ensure the cache directory is accessible
	if err := os.MkdirAll(*cacheDir, os.ModePerm); err != nil {
		log.Fatalf("Failed to make cache directory: %v", err)
	}
	// Register the handler
	http.HandleFunc("/resolve", api.Handler(FindRegistryCommitInit, cratesregistryservice.FindRegistryCommit))
	log.Println("Registry service listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
