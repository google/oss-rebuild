// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"net/url"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// RepoMetrics persists per-repository size/history measurements.
//
// The document ID is the url.PathEscape of the canonical repo URI:
// reversible, legible in the console, and free of the "/" Firestore forbids.
type RepoMetrics = Resource[schema.RepoMetrics, string]

const repoMetricsCollection = "repo_metrics"

func repoMetricsPath(m schema.RepoMetrics) []string { return repoMetricsKey(m.URI) }

func repoMetricsKey(canonicalURI string) []string {
	return []string{repoMetricsCollection, url.PathEscape(canonicalURI)}
}

// NewFirestoreRepoMetrics returns a Firestore-backed RepoMetrics store.
func NewFirestoreRepoMetrics(c *firestore.Client) RepoMetrics {
	return &firestoreResource[schema.RepoMetrics, string]{client: c, pathFor: repoMetricsPath, pathForKey: repoMetricsKey}
}

// NewMemoryRepoMetrics returns an in-memory RepoMetrics store for tests.
func NewMemoryRepoMetrics() RepoMetrics {
	return &memoryResource[schema.RepoMetrics, string]{data: map[string]schema.RepoMetrics{}, pathFor: repoMetricsPath, pathForKey: repoMetricsKey}
}

// FetchRepoBytes reads the recorded packed size of the repository at repo,
// which need not be canonical. An unmeasured repository reads 0.
func FetchRepoBytes(ctx context.Context, store RepoMetrics, repo string) (int64, error) {
	canonical, err := uri.CanonicalizeRepoURI(repo)
	if err != nil {
		return 0, err
	}
	m, err := store.Get(ctx, canonical)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return m.Bytes, nil
}
