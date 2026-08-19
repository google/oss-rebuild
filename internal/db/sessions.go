// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"slices"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/iterator"
)

// Sessions stores agent sessions. ListNonTerminal is the reaper's query,
// the one read keyed on status rather than on a session ID.
type Sessions interface {
	Resource[schema.AgentSession, string]
	// ListNonTerminal returns sessions still INITIALIZING or RUNNING.
	ListNonTerminal(ctx context.Context) ([]schema.AgentSession, error)
}

const sessionCollection = "agent_sessions"

func sessionPath(s schema.AgentSession) []string { return sessionKeyPath(s.ID) }

func sessionKeyPath(id string) []string { return []string{sessionCollection, id} }

// nonTerminalSessionStatuses are the statuses a live execution still owns.
var nonTerminalSessionStatuses = []string{schema.AgentSessionStatusInitializing, schema.AgentSessionStatusRunning}

type firestoreSessions struct {
	*firestoreResource[schema.AgentSession, string]
	client *firestore.Client
}

func NewFirestoreSessions(c *firestore.Client) Sessions {
	return &firestoreSessions{&firestoreResource[schema.AgentSession, string]{client: c, pathFor: sessionPath, pathForKey: sessionKeyPath}, c}
}

func (f *firestoreSessions) ListNonTerminal(ctx context.Context) ([]schema.AgentSession, error) {
	it := f.client.Collection(sessionCollection).Where("status", "in", nonTerminalSessionStatuses).Documents(ctx)
	defer it.Stop()
	var out []schema.AgentSession
	for snap, err := range iterx.ToSeq2(it, iterator.Done) {
		if err != nil {
			return nil, err
		}
		var s schema.AgentSession
		if err := snap.DataTo(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

type memorySessions struct {
	*memoryResource[schema.AgentSession, string]
}

func NewMemorySessions() Sessions {
	return &memorySessions{&memoryResource[schema.AgentSession, string]{data: map[string]schema.AgentSession{}, pathFor: sessionPath, pathForKey: sessionKeyPath}}
}

func (m *memorySessions) ListNonTerminal(ctx context.Context) ([]schema.AgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []schema.AgentSession
	for _, s := range m.data {
		if slices.Contains(nonTerminalSessionStatuses, s.Status) {
			out = append(out, s)
		}
	}
	return out, nil
}
