// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/iterator"
)

// ScratchExecs persists scratch-exec records. The broker inserts pending
// execs; all terminal transitions go through Finalize. The interface
// deliberately omits the raw Resource write methods: exec records race
// concurrent finalizers (agent polls, the reaper, optimistic waits) and a
// full-record overwrite from a stale base would clobber a committed
// terminal state.
type ScratchExecs interface {
	Get(ctx context.Context, id string) (schema.ScratchExec, error)
	Insert(ctx context.Context, v schema.ScratchExec) error
	// Finalize commits exec only if the stored record is still Pending,
	// returning the stored record either way. A lost race returns the
	// concurrent winner alongside ErrUnchanged, so terminal states are
	// written exactly once.
	Finalize(ctx context.Context, exec schema.ScratchExec) (schema.ScratchExec, error)
	// ListPending returns all execs with State == Pending. Backed by a
	// single-field index on "state" in Firestore.
	ListPending(ctx context.Context) ([]schema.ScratchExec, error)
}

// finalizeExec implements Finalize over the generic Mutate primitive.
func finalizeExec(ctx context.Context, r Resource[schema.ScratchExec, string], exec schema.ScratchExec) (schema.ScratchExec, error) {
	return r.Mutate(ctx, exec.ID, func(cur schema.ScratchExec) (schema.ScratchExec, error) {
		if cur.State != schema.ScratchExecPending {
			return cur, ErrUnchanged
		}
		return exec, nil
	})
}

const scratchExecCollection = "scratch-execs"

func scratchExecPath(r schema.ScratchExec) []string { return []string{scratchExecCollection, r.ID} }
func scratchExecKey(id string) []string             { return []string{scratchExecCollection, id} }

type firestoreScratchExecs struct {
	*firestoreResource[schema.ScratchExec, string]
	client *firestore.Client
}

// NewFirestoreScratchExecs returns a ScratchExecs backed by Firestore.
func NewFirestoreScratchExecs(c *firestore.Client) ScratchExecs {
	return &firestoreScratchExecs{
		firestoreResource: &firestoreResource[schema.ScratchExec, string]{
			client: c, pathFor: scratchExecPath, pathForKey: scratchExecKey,
		},
		client: c,
	}
}

func (f *firestoreScratchExecs) Finalize(ctx context.Context, exec schema.ScratchExec) (schema.ScratchExec, error) {
	return finalizeExec(ctx, f.firestoreResource, exec)
}

func (f *firestoreScratchExecs) ListPending(ctx context.Context) ([]schema.ScratchExec, error) {
	iter := f.client.Collection(scratchExecCollection).
		Where("state", "==", string(schema.ScratchExecPending)).
		Documents(ctx)
	defer iter.Stop()
	var out []schema.ScratchExec
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var r schema.ScratchExec
		if err := snap.DataTo(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

type memoryScratchExecs struct {
	*memoryResource[schema.ScratchExec, string]
}

// NewMemoryScratchExecs returns an in-memory ScratchExecs for tests.
func NewMemoryScratchExecs() ScratchExecs {
	return &memoryScratchExecs{
		memoryResource: &memoryResource[schema.ScratchExec, string]{
			data: map[string]schema.ScratchExec{}, pathFor: scratchExecPath, pathForKey: scratchExecKey,
		},
	}
}

func (m *memoryScratchExecs) Finalize(ctx context.Context, exec schema.ScratchExec) (schema.ScratchExec, error) {
	return finalizeExec(ctx, m.memoryResource, exec)
}

func (m *memoryScratchExecs) ListPending(ctx context.Context) ([]schema.ScratchExec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []schema.ScratchExec
	for _, r := range m.data {
		if r.State == schema.ScratchExecPending {
			out = append(out, r)
		}
	}
	return out, nil
}
