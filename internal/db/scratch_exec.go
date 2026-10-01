// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"sort"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/iterator"
)

// ScratchExecs persists scratch-exec records. The broker inserts
// pending execs and updates them as the worker progresses. The reaper
// enumerates pending execs via ListPending.
type ScratchExecs interface {
	Resource[schema.ScratchExec, string]
	// ListPending returns all execs with State == Pending. Backed by a
	// single-field index on "state" in Firestore.
	ListPending(ctx context.Context) ([]schema.ScratchExec, error)
	// ListByScratch returns every exec dispatched to one scratch, ordered
	// by CreatedAt.
	ListByScratch(ctx context.Context, scratchID string) ([]schema.ScratchExec, error)
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

func (f *firestoreScratchExecs) ListByScratch(ctx context.Context, scratchID string) ([]schema.ScratchExec, error) {
	iter := f.client.Collection(scratchExecCollection).
		Where("scratch_id", "==", scratchID).
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
	sortExecsByCreation(out)
	return out, nil
}

func sortExecsByCreation(execs []schema.ScratchExec) {
	sort.Slice(execs, func(i, j int) bool {
		if !execs[i].CreatedAt.Equal(execs[j].CreatedAt) {
			return execs[i].CreatedAt.Before(execs[j].CreatedAt)
		}
		return execs[i].ID < execs[j].ID
	})
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

func (m *memoryScratchExecs) ListByScratch(ctx context.Context, scratchID string) ([]schema.ScratchExec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []schema.ScratchExec
	for _, r := range m.data {
		if r.ScratchID == scratchID {
			out = append(out, r)
		}
	}
	sortExecsByCreation(out)
	return out, nil
}
