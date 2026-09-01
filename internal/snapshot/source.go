// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// Source is the read side of the rollup: it yields the raw records the
// snapshot is built from. Scanning is isolated behind this interface so the
// derivations can be tested with in-memory fixtures.
type Source interface {
	Attempts(context.Context) ([]schema.RebuildAttempt, error)
	Runs(context.Context) ([]schema.Run, error)
	Sessions(context.Context) ([]schema.AgentSession, error)
	Iterations(context.Context) ([]schema.AgentIteration, error)
	Scratches(context.Context) ([]schema.Scratch, error)
	Execs(context.Context) ([]schema.ScratchExec, error)
	RepoMetrics(context.Context) ([]schema.RepoMetrics, error)
}

// FirestoreSource scans a project's Firestore, reusing the same collection and
// collection-group access patterns as internal/rundex.
// TODO: Full scans outlive Firestore's ~15m query lifetime at scale. Move to
// incremental rollups from the previous database, with rarer full scans,
// potentially via BigQuery.
type FirestoreSource struct {
	client *firestore.Client
}

var _ Source = (*FirestoreSource)(nil)

// NewFirestoreSource creates a FirestoreSource for the given GCP project.
func NewFirestoreSource(ctx context.Context, project string) (*FirestoreSource, error) {
	if project == "" {
		return nil, errors.New("empty project provided")
	}
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &FirestoreSource{client: client}, nil
}

// Close releases the underlying Firestore client.
func (s *FirestoreSource) Close() error { return s.client.Close() }

// scanQuery reads every document of a query into a typed slice.
func scanQuery[T any](ctx context.Context, q firestore.Query) ([]T, error) {
	var out []T
	iter := q.Documents(ctx)
	for doc, err := range iterx.ToSeq2(iter, iterator.Done) {
		if err != nil {
			return nil, errors.Wrap(err, "iterating documents")
		}
		var v T
		if err := doc.DataTo(&v); err != nil {
			return nil, errors.Wrap(err, "decoding document")
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *FirestoreSource) Attempts(ctx context.Context) ([]schema.RebuildAttempt, error) {
	return scanQuery[schema.RebuildAttempt](ctx, s.client.CollectionGroup("attempts").Query)
}

func (s *FirestoreSource) Runs(ctx context.Context) ([]schema.Run, error) {
	return scanQuery[schema.Run](ctx, s.client.Collection("runs").Query)
}

func (s *FirestoreSource) Sessions(ctx context.Context) ([]schema.AgentSession, error) {
	return scanQuery[schema.AgentSession](ctx, s.client.Collection("agent_sessions").Query)
}

func (s *FirestoreSource) Iterations(ctx context.Context) ([]schema.AgentIteration, error) {
	// Iterations live in agent_sessions/{id}/agent_iterations. A collection
	// group query gathers them all in one scan (each carries session_id).
	return scanQuery[schema.AgentIteration](ctx, s.client.CollectionGroup("agent_iterations").Query)
}

func (s *FirestoreSource) Scratches(ctx context.Context) ([]schema.Scratch, error) {
	return scanQuery[schema.Scratch](ctx, s.client.Collection("scratch").Query)
}

func (s *FirestoreSource) Execs(ctx context.Context) ([]schema.ScratchExec, error) {
	return scanQuery[schema.ScratchExec](ctx, s.client.Collection("scratch-execs").Query)
}

func (s *FirestoreSource) RepoMetrics(ctx context.Context) ([]schema.RepoMetrics, error) {
	return scanQuery[schema.RepoMetrics](ctx, s.client.Collection("repo_metrics").Query)
}
