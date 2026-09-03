// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// IterationKey is the primary key for an AgentIteration: iterations live in
// a subcollection of their session.
type IterationKey struct {
	SessionID string
	ID        string
}

func iterationPath(i schema.AgentIteration) []string {
	return iterationKeyPath(IterationKey{SessionID: i.SessionID, ID: i.ID})
}

func iterationKeyPath(k IterationKey) []string {
	return []string{"agent_sessions", k.SessionID, "agent_iterations", k.ID}
}

func NewFirestoreIterations(c *firestore.Client) Iterations {
	return &firestoreResource[schema.AgentIteration, IterationKey]{client: c, pathFor: iterationPath, pathForKey: iterationKeyPath}
}

func NewMemoryIterations() Iterations {
	return &memoryResource[schema.AgentIteration, IterationKey]{data: map[string]schema.AgentIteration{}, pathFor: iterationPath, pathForKey: iterationKeyPath}
}
