// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

// SourceLocation identifies a file within a git repository at a specific
// ref. It mirrors attestation.SourceLocation, which lacks the firestore
// tags needed for persistence. Keep the two in sync.
type SourceLocation struct {
	Repository string `json:"repository" firestore:"repository"`         // source repository URI
	Ref        string `json:"ref" firestore:"ref"`                       // resolved commit hash
	Path       string `json:"path,omitempty" firestore:"path,omitempty"` // repo-relative file path
}

// InferenceRun records an execution of the inference service.
type InferenceRun struct {
	Version string `json:"version" firestore:"version"` // service-reported version, possibly empty
}

// StrategyProvenance describes the inputs that produced an executed
// strategy: a build definition, an inference run, or both when a
// LocationHint seeded inference. Interpretations of these inputs (e.g.
// manual vs inferred) are left to consumers. Nil indicates strategy
// resolution never completed or predated provenance capture.
type StrategyProvenance struct {
	Definition *SourceLocation `json:"definition,omitempty" firestore:"definition,omitempty"` // build def entry, pinned at the serving ref
	Inference  *InferenceRun   `json:"inference,omitempty" firestore:"inference,omitempty"`   // set iff inference ran
}
