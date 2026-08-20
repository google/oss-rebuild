// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"time"
)

// TokenUsage records LLM token consumption, bucketed by billing
// treatment: cached input is discounted and thinking bills as output.
type TokenUsage struct {
	Input       int    `json:"input,omitempty" firestore:"input,omitempty"`               // includes cached and tool-use prompt tokens
	CachedInput int    `json:"cached_input,omitempty" firestore:"cached_input,omitempty"` // subset of Input
	Output      int    `json:"output,omitempty" firestore:"output,omitempty"`             // includes thinking tokens
	Model       string `json:"model,omitempty" firestore:"model,omitempty"`
}

// Add returns the sum of u and other. Both must name the same model since a
// sum carries a single label and is priced at that model's rates.
// TODO: Track per-model subtotals so one session can mix models.
func (u TokenUsage) Add(other TokenUsage) TokenUsage {
	if u.Model != "" && other.Model != "" && u.Model != other.Model {
		panic(fmt.Sprintf("summing TokenUsage across models %q and %q", u.Model, other.Model))
	}
	model := u.Model
	if model == "" {
		model = other.Model
	}
	return TokenUsage{
		Input:       u.Input + other.Input,
		CachedInput: u.CachedInput + other.CachedInput,
		Output:      u.Output + other.Output,
		Model:       model,
	}
}

// Sub returns u minus other, component-wise, keeping u's model label. It
// isolates the tokens spent between two snapshots of a running total.
func (u TokenUsage) Sub(other TokenUsage) TokenUsage {
	return TokenUsage{
		Input:       u.Input - other.Input,
		CachedInput: u.CachedInput - other.CachedInput,
		Output:      u.Output - other.Output,
		Model:       u.Model,
	}
}

// OrNil returns a pointer to u, or nil when no tokens were counted so that
// omitempty fields stay unset.
func (u TokenUsage) OrNil() *TokenUsage {
	if u.Input == 0 && u.CachedInput == 0 && u.Output == 0 {
		return nil
	}
	return &u
}

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
