// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
)

var (
	cacheDir                  = flag.String("cache-dir", "/tmp/registry-cache", "Directory to cache registry indices")
	maxSnapshots              = flag.Int("max-snapshots", 4, "Maximum number of snapshot indices to cache")
	currentUpdateIntervalMins = flag.Int("current-update-interval-mins", 30, "Update interval for current index in minutes")
	gitCacheURL               = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
)

var indexManager *index.IndexManager

func init() {
	flag.Parse()
	// Ensure cache directory exists
	err := os.MkdirAll(*cacheDir, 0755)
	if err != nil {
		log.Fatal(errors.Wrap(err, "failed to create cache directory"))
	}
	// Create filesystem for cache directory
	fs := osfs.New(*cacheDir)
	// Setup git-cache if provided
	var currentCloneFunc, snapshotCloneFunc gitx.CloneFunc
	if *gitCacheURL != "" {
		ctx := context.Background()
		c, err := idtoken.NewClient(ctx, *gitCacheURL)
		if err != nil {
			log.Fatal(errors.Wrap(err, "creating git cache id client"))
		}
		sc, _, err := gapihttp.NewClient(ctx)
		if err != nil {
			log.Fatal(errors.Wrap(err, "creating git cache API client"))
		}
		u, err := url.Parse(*gitCacheURL)
		if err != nil {
			log.Fatal(errors.Wrap(err, "parsing git cache URL"))
		}
		// For current index: skip cache (changes frequently)
		// TODO: We should use the cache but would want to coordinate an update time, not a fixed freshness.
		currentCloneFunc = gitx.Clone
		// For snapshots: use cache with infinite freshness (immutable)
		snapshotGitCache := &gitx.Cache{IDClient: c, APIClient: sc, URL: u, DefaultFreshness: time.Time{}}
		snapshotCloneFunc = snapshotGitCache.Clone
	}
	cfg := index.IndexManagerConfig{
		Filesystem:            fs,
		MaxSnapshots:          *maxSnapshots,
		CurrentUpdateInterval: time.Duration(*currentUpdateIntervalMins) * time.Minute,
		CurrentCloneFunc:      currentCloneFunc,
		SnapshotCloneFunc:     snapshotCloneFunc,
	}
	indexManager, err = index.NewIndexManagerFromFS(cfg)
	if err != nil {
		log.Fatal(errors.Wrap(err, "failed to create index manager"))
	}
}

func FindRegistryCommitInit(ctx context.Context) (*cratesregistryservice.FindRegistryCommitDeps, error) {
	return &cratesregistryservice.FindRegistryCommitDeps{IndexManager: indexManager}, nil
}

func main() {
	// Register the handler
	http.HandleFunc("/resolve", api.Handler(FindRegistryCommitInit, cratesregistryservice.FindRegistryCommit))
	log.Println("Registry service listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
