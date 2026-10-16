// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/dump"
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
	port                      = flag.Int("port", 8080, "port on which to serve")
)

var indexManager *index.IndexManager

// snapshots caches the archive's snapshot listing across requests.
var snapshots = &index.SnapshotList{TTL: time.Hour, List: index.ListAvailableSnapshots}

// crates is the latest crates.io dump, nil until the first one loads.
var crates atomic.Pointer[dump.Index]

// dumpCheck paces checks for a new crates.io dump, which is published daily.
const dumpCheck = time.Hour

// loadDumps keeps crates on the latest published dump.
func loadDumps(ctx context.Context) {
	for {
		latest, err := dump.Latest(ctx, http.DefaultClient)
		if err != nil {
			log.Printf("checking for a crates.io dump: %v", err)
		} else if cur := crates.Load(); cur == nil || cur.ETag != latest {
			// Two dumps do not fit beside the index clones this service holds
			// in memory, so lookups go live while the new one loads.
			crates.Store(nil)
			start := time.Now()
			if x, err := dump.Fetch(ctx, http.DefaultClient); err != nil {
				log.Printf("loading the crates.io dump: %v", err)
			} else {
				crates.Store(x)
				log.Printf("loaded crates.io dump %s in %s", x.ETag, time.Since(start).Round(time.Second))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(dumpCheck):
		}
	}
}

func MetadataInit(context.Context) (*cratesregistryservice.MetadataDeps, error) {
	return &cratesregistryservice.MetadataDeps{Dump: crates.Load()}, nil
}

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
		snapshotGitCache := &gitcache.Client{IDClient: c, APIClient: sc, URL: u, DefaultFreshness: time.Time{}}
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
	return &cratesregistryservice.FindRegistryCommitDeps{IndexManager: indexManager, ListSnapshots: snapshots.Get}, nil
}

// serveDump loads the crates.io dump and serves /crate and /version from it.
// While it is off, callers read crate metadata from crates.io.
const serveDump = true

func main() {
	// Register the handler
	http.HandleFunc("/resolve", api.Handler(FindRegistryCommitInit, cratesregistryservice.FindRegistryCommit))
	http.Handle("/readyz", httpegress.ReadyHandler(http.DefaultClient))
	if serveDump {
		http.HandleFunc("/crate", api.Handler(MetadataInit, cratesregistryservice.Crate))
		http.HandleFunc("/version", api.Handler(MetadataInit, cratesregistryservice.Version))
		go loadDumps(context.Background())
	}
	log.Printf("Registry service listening on :%d\n", *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
