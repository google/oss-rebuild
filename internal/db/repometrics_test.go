// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestMemoryRepoMetrics_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryRepoMetrics()
	canonical, err := uri.CanonicalizeRepoURI("git@github.com:Org/Repo.git")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := schema.RepoMetrics{
		URI:        canonical,
		Bytes:      4096,
		Commits:    12,
		Head:       "deadbeef",
		MeasuredAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := s.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// The ssh and https aliases canonicalize to the same key and document.
	key, err := uri.CanonicalizeRepoURI("https://github.com/org/repo")
	if err != nil {
		t.Fatalf("canonicalize alias: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("Get = %+v; want %+v", got, want)
	}
	updated := want
	updated.Bytes = 8192
	if err := s.Upsert(ctx, updated); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}
	got, err = s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Bytes != 8192 {
		t.Errorf("after re-Upsert Get = %+v; want Bytes=8192", got)
	}
}

func TestRepoMetricsKey(t *testing.T) {
	got := repoMetricsKey("https://github.com/org/repo")
	want := []string{"repo_metrics", "https:%2F%2Fgithub.com%2Forg%2Frepo"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("repoMetricsKey = %v; want %v", got, want)
	}
}

func TestMemoryRepoMetrics_NotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryRepoMetrics()
	if _, err := s.Get(ctx, "https://github.com/missing/repo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v; want ErrNotFound", err)
	}
}
