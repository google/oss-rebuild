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
	"strconv"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/pkg/errors"
	"google.golang.org/api/idtoken"
	gapihttp "google.golang.org/api/transport/http"
)

var (
	gitCacheURL       = flag.String("git-cache-url", "", "if provided, the git-cache service to use to fetch repos")
	cratesRegistryURL = flag.String("crates-registry-service-url", "", "if provided, the crates registry service to use for Rust crate index resolution")
	project           = flag.String("project", "", "if provided, the GCP project whose Firestore holds repo_metrics records")
	maxRepoBytes      = flag.Int64("max-repo-bytes", 0, "git-cache fetch budget in bytes (0 = no limit)")
	port              = flag.Int("port", 8080, "port on which to serve")
)

var httpcfg = httpegress.Config{}

// InferInit returns deps shared by all requests.
func InferInit(ctx context.Context) (*inferenceservice.InferDeps, error) {
	var d inferenceservice.InferDeps
	var err error
	if *project != "" {
		fs, err := firestore.NewClient(ctx, *project)
		if err != nil {
			return nil, errors.Wrap(err, "creating firestore client")
		}
		d.RepoMetrics = db.NewFirestoreRepoMetrics(fs)
	}
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	if *gitCacheURL != "" {
		c, err := idtoken.NewClient(ctx, *gitCacheURL)
		if err != nil {
			return nil, errors.Wrap(err, "creating git cache id client")
		}
		sc, _, err := gapihttp.NewClient(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "creating git cache API client")
		}
		u, err := url.Parse(*gitCacheURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing git cache URL")
		}
		d.GitCache = &gitcache.Client{IDClient: c, APIClient: sc, URL: u, MaxBytes: *maxRepoBytes}
	}
	d.RepoOptF = func() *gitx.RepositoryOptions {
		return &gitx.RepositoryOptions{
			Worktree: memfs.New(),
			Storer:   gitx.NewInMemoryStorer(),
		}
	}
	if *cratesRegistryURL != "" {
		u, err := url.Parse(*cratesRegistryURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing crates registry service URL")
		}
		c, err := idtoken.NewClient(ctx, *cratesRegistryURL)
		if err != nil {
			return nil, errors.Wrap(err, "creating crates registry id client")
		}
		d.CratesRegistryStub = api.Stub[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse](c, u.JoinPath("resolve"))
	}
	if total, err := memTotal(); err != nil {
		log.Printf("requests reserve no memory: %v", err)
	} else if d.Memory = inferenceservice.NewMemory(total); d.Memory == nil {
		log.Printf("requests reserve no memory: %d MiB is within base memory", total>>20)
	}
	return &d, nil
}

// memTotal reads MemTotal, which Cloud Run sets to the container limit.
func memTotal() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(b)) {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10, errors.Wrap(err, "parsing MemTotal")
		}
	}
	return 0, errors.New("no MemTotal in /proc/meminfo")
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	deps, err := InferInit(context.Background())
	if err != nil {
		log.Fatalln(errors.Wrap(err, "initializing dependencies"))
	}
	http.HandleFunc("/infer", api.Handler(func(context.Context) (*inferenceservice.InferDeps, error) { return deps, nil }, inferenceservice.Infer))
	http.HandleFunc("/version", api.Handler(api.NoDepsInit, inferenceservice.Version))
	http.Handle("/readyz", httpegress.ReadyHandler(http.DefaultClient))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalln(err)
	}
}
