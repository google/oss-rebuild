// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Scratch persists scratch lifecycle records. Extends the generic Resource
// CRUD with field-level mutators and lifecycle-specific queries.
//
// UpdateState and UpdateLastUsed are not just convenience over Update; they
// write a single Firestore field each, which gives field-level atomicity
// against concurrent writers. A full-record Update from the reaper and a
// concurrent UpdateLastUsed from an in-flight exec would race; whichever
// landed last would clobber the other's field. With field-level updates the
// two writes commute.
type Scratch interface {
	Resource[schema.Scratch, string]
	// UpdateState sets the state field (and bumps `updated`).
	UpdateState(ctx context.Context, scratchID string, s schema.ScratchState) error
	// UpdateLastUsed sets the last_used field (and bumps `updated`).
	UpdateLastUsed(ctx context.Context, scratchID string, t time.Time) error
	// ListIdleSince returns scratches in state Ready whose LastUsed is
	// strictly before t. Backed by a (state, last_used) composite index in
	// Firestore.
	ListIdleSince(ctx context.Context, t time.Time) ([]schema.Scratch, error)
	Delete(ctx context.Context, scratchID string) error
}

const scratchCollection = "scratch"

func scratchPath(s schema.Scratch) []string { return []string{scratchCollection, s.ID} }
func scratchKey(id string) []string         { return []string{scratchCollection, id} }

type firestoreScratch struct {
	*firestoreResource[schema.Scratch, string]
	client *firestore.Client
}

func NewFirestoreScratch(c *firestore.Client) Scratch {
	return &firestoreScratch{
		firestoreResource: &firestoreResource[schema.Scratch, string]{
			client: c, pathFor: scratchPath, pathForKey: scratchKey,
		},
		client: c,
	}
}

func (f *firestoreScratch) doc(id string) *firestore.DocumentRef {
	return f.client.Collection(scratchCollection).Doc(id)
}

func (f *firestoreScratch) UpdateState(ctx context.Context, scratchID string, s schema.ScratchState) error {
	_, err := f.doc(scratchID).Update(ctx, []firestore.Update{
		{Path: "state", Value: s},
		{Path: "updated", Value: time.Now().UTC()},
	})
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

func (f *firestoreScratch) UpdateLastUsed(ctx context.Context, scratchID string, t time.Time) error {
	_, err := f.doc(scratchID).Update(ctx, []firestore.Update{
		{Path: "last_used", Value: t.UTC()},
		{Path: "updated", Value: time.Now().UTC()},
	})
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

func (f *firestoreScratch) ListIdleSince(ctx context.Context, t time.Time) ([]schema.Scratch, error) {
	q := f.client.Collection(scratchCollection).
		Where("state", "==", string(schema.ScratchReady)).
		Where("last_used", "<", t.UTC())
	iter := q.Documents(ctx)
	defer iter.Stop()
	var out []schema.Scratch
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var e schema.Scratch
		if err := snap.DataTo(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *firestoreScratch) Delete(ctx context.Context, scratchID string) error {
	// Firestore Delete on a missing doc is a no-op; wrap in a tx so we can
	// surface ErrNotFound.
	err := f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ref := f.doc(scratchID)
		if _, err := tx.Get(ref); err != nil {
			return err
		}
		return tx.Delete(ref)
	})
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

type memoryScratch struct {
	*memoryResource[schema.Scratch, string]
}

// NewMemoryScratch returns an in-memory Scratch for tests.
func NewMemoryScratch() Scratch {
	return &memoryScratch{
		memoryResource: &memoryResource[schema.Scratch, string]{
			data: map[string]schema.Scratch{}, pathFor: scratchPath, pathForKey: scratchKey,
		},
	}
}

func (m *memoryScratch) UpdateState(ctx context.Context, scratchID string, s schema.ScratchState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scratchCollection + "/" + scratchID
	e, ok := m.data[key]
	if !ok {
		return ErrNotFound
	}
	e.State = s
	e.Updated = time.Now().UTC()
	m.data[key] = e
	return nil
}

func (m *memoryScratch) UpdateLastUsed(ctx context.Context, scratchID string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scratchCollection + "/" + scratchID
	e, ok := m.data[key]
	if !ok {
		return ErrNotFound
	}
	e.LastUsed = t.UTC()
	e.Updated = time.Now().UTC()
	m.data[key] = e
	return nil
}

func (m *memoryScratch) ListIdleSince(ctx context.Context, t time.Time) ([]schema.Scratch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []schema.Scratch
	for _, e := range m.data {
		if e.State == schema.ScratchReady && e.LastUsed.Before(t) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memoryScratch) Delete(ctx context.Context, scratchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scratchCollection + "/" + scratchID
	if _, ok := m.data[key]; !ok {
		return ErrNotFound
	}
	delete(m.data, key)
	return nil
}
