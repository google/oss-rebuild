// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import "time"

// RepoMetrics records size measurements of a source repository, taken on
// clone by the inference service. Keyed by repo rather than target since
// many packages share a repository.
type RepoMetrics struct {
	URI        string    `json:"uri,omitempty" firestore:"uri,omitempty"`         // canonical repo URI
	Bytes      int64     `json:"bytes,omitempty" firestore:"bytes,omitempty"`     // packed object store bytes at clone
	Commits    int64     `json:"commits,omitempty" firestore:"commits,omitempty"` // commit objects in the clone
	Head       string    `json:"head,omitempty" firestore:"head,omitempty"`       // head commit hash at measurement
	MeasuredAt time.Time `json:"measured_at,omitzero" firestore:"measured_at,omitempty"`
}
