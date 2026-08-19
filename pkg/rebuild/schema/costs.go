// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import "time"

// AttemptCosts records the measured resource costs of a single rebuild
// attempt in raw units (seconds/bytes). Repository size/history lives in
// RepoMetrics, joined by repo URI.
type AttemptCosts struct {
	// Inference.
	InferenceSeconds float64 `json:"inference_seconds,omitempty" firestore:"inference_seconds,omitempty"`
	Tokens           int64   `json:"tokens,omitempty" firestore:"tokens,omitempty"` // total tokens used, if an agentic attempt
	// Build execution, end to end.
	BuilderSeconds float64  `json:"builder_seconds,omitempty" firestore:"builder_seconds,omitempty"`
	BuilderPool    SizeHint `json:"builder_pool,omitempty" firestore:"builder_pool,omitempty"` // effective pool derives downstream
	// Storage.
	LogsBytes      int64 `json:"logs_bytes,omitempty" firestore:"logs_bytes,omitempty"`
	ContainerBytes int64 `json:"container_bytes,omitempty" firestore:"container_bytes,omitempty"`
	ArtifactBytes  int64 `json:"artifact_bytes,omitempty" firestore:"artifact_bytes,omitempty"`
}

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
