// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// Source is the read side of the snapshot pipelines: each scan yields the
// records written at or after the given watermark, and FullScan yields
// every record. Scanning is isolated behind this interface so the
// derivations can be tested with in-memory fixtures.
type Source interface {
	Attempts(context.Context, time.Time) ([]schema.RebuildAttempt, error)
	Runs(context.Context, time.Time) ([]schema.Run, error)
	Sessions(context.Context, time.Time) ([]schema.AgentSession, error)
	Iterations(context.Context, time.Time) ([]schema.AgentIteration, error)
	Scratches(context.Context, time.Time) ([]schema.Scratch, error)
	Execs(context.Context, time.Time) ([]schema.ScratchExec, error)
	RepoMetrics(context.Context, time.Time) ([]schema.RepoMetrics, error)
}

// FullScan is the zero watermark: a scan given it reads every record.
var FullScan time.Time

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

// sinceQuery bounds q to documents whose clock field is at or after since.
// A zero since deliberately leaves the query unfiltered.
func sinceQuery(q firestore.Query, clock string, since time.Time) firestore.Query {
	if since.IsZero() {
		return q
	}
	return q.Where(clock, ">=", since)
}

func (s *FirestoreSource) Attempts(ctx context.Context, since time.Time) ([]schema.RebuildAttempt, error) {
	return scanQuery[schema.RebuildAttempt](ctx, sinceQuery(s.client.CollectionGroup("attempts").Query, "updated", since))
}

// Runs bound on created: runs are write-once, so creation time is the only
// clock they carry.
func (s *FirestoreSource) Runs(ctx context.Context, since time.Time) ([]schema.Run, error) {
	return scanRuns(ctx, sinceQuery(s.client.Collection("runs").Query, "created", since))
}

// scanRuns reads run documents, recovering the ID from the document ref for
// historical entries that only carry it there.
func scanRuns(ctx context.Context, q firestore.Query) ([]schema.Run, error) {
	var out []schema.Run
	iter := q.Documents(ctx)
	for doc, err := range iterx.ToSeq2(iter, iterator.Done) {
		if err != nil {
			return nil, errors.Wrap(err, "iterating runs")
		}
		var r schema.Run
		if err := doc.DataTo(&r); err != nil {
			return nil, errors.Wrap(err, "decoding run")
		}
		if r.ID == "" {
			r.ID = doc.Ref.ID
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *FirestoreSource) Sessions(ctx context.Context, since time.Time) ([]schema.AgentSession, error) {
	return scanQuery[schema.AgentSession](ctx, sinceQuery(s.client.Collection("agent_sessions").Query, "updated", since))
}

func (s *FirestoreSource) Iterations(ctx context.Context, since time.Time) ([]schema.AgentIteration, error) {
	// Iterations live in agent_sessions/{id}/agent_iterations. A collection
	// group query gathers them all in one scan (each carries session_id).
	return scanQuery[schema.AgentIteration](ctx, sinceQuery(s.client.CollectionGroup("agent_iterations").Query, "updated", since))
}

func (s *FirestoreSource) Scratches(ctx context.Context, since time.Time) ([]schema.Scratch, error) {
	return scanQuery[schema.Scratch](ctx, sinceQuery(s.client.Collection("scratch").Query, "updated", since))
}

func (s *FirestoreSource) Execs(ctx context.Context, since time.Time) ([]schema.ScratchExec, error) {
	return scanQuery[schema.ScratchExec](ctx, sinceQuery(s.client.Collection("scratch-execs").Query, "updated", since))
}

func (s *FirestoreSource) RepoMetrics(ctx context.Context, since time.Time) ([]schema.RepoMetrics, error) {
	return scanQuery[schema.RepoMetrics](ctx, sinceQuery(s.client.Collection("repo_metrics").Query, "updated", since))
}
