// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// AttemptKey is the primary key for a RebuildAttempt: a target and the run
// during which the attempt was made.
type AttemptKey struct {
	Target rebuild.Target
	RunID  string
}

func attemptPath(a schema.RebuildAttempt) []string {
	return attemptKeyPath(AttemptKey{Target: a.Target(), RunID: a.RunID})
}

func attemptKeyPath(k AttemptKey) []string {
	et := rebuild.FirestoreTargetEncoding.Encode(k.Target)
	return []string{
		"ecosystem", string(et.Ecosystem),
		"packages", et.Package,
		"versions", et.Version,
		"artifacts", et.Artifact,
		"attempts", k.RunID,
	}
}

func NewFirestoreAttempts(c *firestore.Client) Attempts {
	return &firestoreResource[schema.RebuildAttempt, AttemptKey]{client: c, pathFor: attemptPath, pathForKey: attemptKeyPath}
}

func NewMemoryAttempts() Attempts {
	return &memoryResource[schema.RebuildAttempt, AttemptKey]{data: map[string]schema.RebuildAttempt{}, pathFor: attemptPath, pathForKey: attemptKeyPath}
}
