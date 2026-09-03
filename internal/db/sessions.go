// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func sessionPath(s schema.AgentSession) []string { return sessionKeyPath(s.ID) }

func sessionKeyPath(id string) []string { return []string{"agent_sessions", id} }

func NewFirestoreSessions(c *firestore.Client) Sessions {
	return &firestoreResource[schema.AgentSession, string]{client: c, pathFor: sessionPath, pathForKey: sessionKeyPath}
}

func NewMemorySessions() Sessions {
	return &memoryResource[schema.AgentSession, string]{data: map[string]schema.AgentSession{}, pathFor: sessionPath, pathForKey: sessionKeyPath}
}
