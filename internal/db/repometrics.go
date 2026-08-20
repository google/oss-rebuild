// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"net/url"

	"cloud.google.com/go/firestore"
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
