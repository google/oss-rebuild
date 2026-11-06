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
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	head, err := repo.GoGit.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	store := db.NewMemoryRepoMetrics()
	// The non-canonical ssh alias exercises canonicalization on write.
	fetched := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	rcfg := rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository, Storer: repo.Storer, FetchedAt: fetched}, URI: "git@github.com:Org/Repo.git"}
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
	if err := recordRepoMetrics(context.Background(), store, rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository, Storer: repo.Storer}, URI: ""}); err == nil {
		t.Error("recordRepoMetrics(empty URI) = nil error; want error")
	}
}

func TestRecordRepoMetricsNilRepository(t *testing.T) {
	store := db.NewMemoryRepoMetrics()
	if err := recordRepoMetrics(context.Background(), store, rebuild.RepoConfig{URI: "https://github.com/org/repo"}); err == nil {
		t.Error("recordRepoMetrics(nil repository) = nil error; want error")
	}
}

func TestFailureStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
		kind rebuild.FailureKind
	}{
		{name: "Deadline", err: errors.Wrap(context.DeadlineExceeded, "walking history"), code: codes.DeadlineExceeded},
		{name: "TooLarge", err: errors.Wrap(&gitx.RepoTooLargeError{URL: "https://github.com/org/big", Limit: 4 << 30}, "clone failed [repo=https://github.com/org/big]"), code: codes.FailedPrecondition, kind: rebuild.FailureOversize},
		{name: "Unclassified", err: errors.New("object not found"), code: codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := failureStatus(tc.err)
			s := status.Convert(got)
			if s.Code() != tc.code || s.Message() != tc.err.Error() {
				t.Errorf("failureStatus() = %v %q, want %v %q", s.Code(), s.Message(), tc.code, tc.err.Error())
			}
			d, ok := api.DetailOf[rebuild.InferenceErrorDetail](got)
			if d.Kind != tc.kind || ok != (tc.kind != "") {
				t.Errorf("kind = %q (present %t), want %q", d.Kind, ok, tc.kind)
			}
		})
	}
}
