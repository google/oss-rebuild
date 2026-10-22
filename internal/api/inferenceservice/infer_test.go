// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
)

type cratesIORegistryStub struct {
	cratesio.Registry
	version      *cratesio.CrateVersion
	versionCalls int
}

func (r *cratesIORegistryStub) Version(context.Context, string, string) (*cratesio.CrateVersion, error) {
	r.versionCalls++
	return r.version, nil
}

func (r *cratesIORegistryStub) Crate(context.Context, string) (*cratesio.Crate, error) {
	return nil, nil
}

func (r *cratesIORegistryStub) Artifact(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func TestWithCratesIOFreshness(t *testing.T) {
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	registry := &cratesIORegistryStub{version: &cratesio.CrateVersion{Version: cratesio.Version{Created: created}}}
	original := &gitcache.Client{
		IDClient:         &http.Client{},
		APIClient:        &http.Client{},
		URL:              &url.URL{},
		DefaultFreshness: created.Add(-time.Hour),
	}
	ctx := context.WithValue(context.Background(), rebuild.RepoCacheClientID, original)
	mux := rebuild.RegistryMux{CratesIO: registry}
	target := rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "demo", Version: "1.0.1"}

	gotCtx, gotMux, err := withCratesIOFreshness(ctx, target, mux)
	if err != nil {
		t.Fatalf("withCratesIOFreshness() error = %v", err)
	}
	gotCache, ok := gotCtx.Value(rebuild.RepoCacheClientID).(*gitcache.Client)
	if !ok || gotCache == original {
		t.Fatal("withCratesIOFreshness() did not copy the cache client")
	}
	if !gotCache.DefaultFreshness.Equal(created) {
		t.Errorf("DefaultFreshness = %v, want %v", gotCache.DefaultFreshness, created)
	}
	if !original.DefaultFreshness.Equal(created.Add(-time.Hour)) {
		t.Errorf("original DefaultFreshness changed to %v", original.DefaultFreshness)
	}
	gotVersion, err := gotMux.CratesIO.Version(context.Background(), target.Package, target.Version)
	if err != nil {
		t.Fatalf("cached Version() error = %v", err)
	}
	if gotVersion != registry.version || registry.versionCalls != 1 {
		t.Errorf("cached Version() = %p, calls = %d; want %p, 1", gotVersion, registry.versionCalls, registry.version)
	}
}

func TestWithCratesIOFreshnessWithoutCacheIsNoop(t *testing.T) {
	registry := &cratesIORegistryStub{}
	target := rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "demo", Version: "1.0.1"}
	ctx, mux, err := withCratesIOFreshness(context.Background(), target, rebuild.RegistryMux{CratesIO: registry})
	if err != nil {
		t.Fatalf("withCratesIOFreshness() error = %v", err)
	}
	if ctx.Value(rebuild.RepoCacheClientID) != nil || mux.CratesIO != registry || registry.versionCalls != 0 {
		t.Error("withCratesIOFreshness() changed context or registry without a cache client")
	}
}

const twoCommitRepo = `
commits:
  - id: first
    branch: master
    message: "first"
    files:
      README.md: "hello"
  - id: second
    parent: first
    branch: master
    message: "second"
    files:
      README.md: "world"
`

func newTestRepo(t *testing.T) *gitxtest.Repository {
	t.Helper()
	// The filesystem-backed storer matches production and is required by
	// gitx.ObjectStoreSize.
	repo, err := gitxtest.CreateRepoFromYAML(twoCommitRepo, &gitxtest.RepositoryOptions{Storer: gitx.NewInMemoryStorer(), Worktree: memfs.New()})
	if err != nil {
		t.Fatalf("CreateRepoFromYAML: %v", err)
	}
	return repo
}

func TestRecordRepoMetrics(t *testing.T) {
	repo := newTestRepo(t)
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	store := db.NewMemoryRepoMetrics()
	// The non-canonical ssh alias exercises canonicalization on write.
	fetched := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	rcfg := rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository, FetchedAt: fetched}, URI: "git@github.com:Org/Repo.git"}
	if err := recordRepoMetrics(context.Background(), store, rcfg); err != nil {
		t.Fatalf("recordRepoMetrics: %v", err)
	}
	got, err := store.Get(context.Background(), "https://github.com/org/repo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.URI != "https://github.com/org/repo" {
		t.Errorf("URI = %q; want canonical https form", got.URI)
	}
	if got.Commits != 2 {
		t.Errorf("Commits = %d; want 2", got.Commits)
	}
	if got.Head != head.Hash().String() {
		t.Errorf("Head = %q; want %q", got.Head, head.Hash().String())
	}
	if got.Bytes <= 0 {
		t.Errorf("Bytes = %d; want > 0", got.Bytes)
	}
	if !got.MeasuredAt.Equal(fetched) {
		t.Errorf("MeasuredAt = %v; want the config's FetchedAt %v", got.MeasuredAt, fetched)
	}
	if got.Updated.IsZero() {
		t.Error("Updated is zero; want a timestamp")
	}
}

func TestRecordRepoMetricsBadURI(t *testing.T) {
	repo := newTestRepo(t)
	store := db.NewMemoryRepoMetrics()
	if err := recordRepoMetrics(context.Background(), store, rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository}, URI: ""}); err == nil {
		t.Error("recordRepoMetrics(empty URI) = nil error; want error")
	}
}

func TestRecordRepoMetricsNilRepository(t *testing.T) {
	store := db.NewMemoryRepoMetrics()
	if err := recordRepoMetrics(context.Background(), store, rebuild.RepoConfig{URI: "https://github.com/org/repo"}); err == nil {
		t.Error("recordRepoMetrics(nil repository) = nil error; want error")
	}
}
