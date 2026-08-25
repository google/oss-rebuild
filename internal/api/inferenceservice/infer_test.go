// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

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
