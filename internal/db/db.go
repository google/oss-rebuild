// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"path"
	"sync"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Resource is a typed CRUD handle for a single Firestore collection (or
// nested-collection path). T is the value type; K is the primary key type.
type Resource[T, K any] interface {
	Get(ctx context.Context, key K) (T, error)
	Insert(ctx context.Context, v T) error
	Update(ctx context.Context, v T) error
	Upsert(ctx context.Context, v T) error
	Mutate(ctx context.Context, key K, fn func(T) (T, error)) (T, error) // Atomic read-modify-write. fn may rerun, return ErrUnchanged to skip write
}

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrUnchanged signals that a Mutate left the stored record as-is.
	ErrUnchanged = errors.New("unchanged")
)

// Type aliases for the resources currently defined.
type (
	Runs     = Resource[schema.Run, string]
	Attempts = Resource[schema.RebuildAttempt, AttemptKey]
)

// firestoreResource is an internal generic implementation of Resource using Firestore.
type firestoreResource[T, K any] struct {
	client     *firestore.Client
	pathFor    func(T) []string // collection, doc, collection, doc, ...
	pathForKey func(K) []string
}

func (r *firestoreResource[T, K]) doc(path []string) *firestore.DocumentRef {
	ref := r.client.Collection(path[0]).Doc(path[1])
	for i := 2; i < len(path); i += 2 {
		ref = ref.Collection(path[i]).Doc(path[i+1])
	}
	return ref
}

func (r *firestoreResource[T, K]) Get(ctx context.Context, k K) (T, error) {
	var v T
	snap, err := r.doc(r.pathForKey(k)).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return v, ErrNotFound
		}
		return v, err
	}
	if err := snap.DataTo(&v); err != nil {
		return v, err
	}
	return v, nil
}

func (r *firestoreResource[T, K]) Insert(ctx context.Context, v T) error {
	_, err := r.doc(r.pathFor(v)).Create(ctx, v)
	if status.Code(err) == codes.AlreadyExists {
		return ErrAlreadyExists
	}
	return err
}

func (r *firestoreResource[T, K]) Update(ctx context.Context, v T) error {
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		dr := r.doc(r.pathFor(v))
		if _, err := tx.Get(dr); err != nil {
			return err
		}
		return tx.Set(dr, v)
	})
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}

func (r *firestoreResource[T, K]) Upsert(ctx context.Context, v T) error {
	_, err := r.doc(r.pathFor(v)).Set(ctx, v)
	return err
}

func (r *firestoreResource[T, K]) Mutate(ctx context.Context, k K, fn func(T) (T, error)) (T, error) {
	var out T
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		dr := r.doc(r.pathForKey(k))
		snap, err := tx.Get(dr)
		if err != nil {
			return err
		}
		var cur T
		if err := snap.DataTo(&cur); err != nil {
			return err
		}
		next, err := fn(cur)
		if err != nil {
			out = cur
			return err
		}
		out = next
		return tx.Set(dr, next)
	})
	if status.Code(err) == codes.NotFound {
		return out, ErrNotFound
	}
	return out, err
}

// memoryResource is an internal generic implementation of Resource using an in-memory map.
type memoryResource[T, K any] struct {
	mu         sync.Mutex
	data       map[string]T
	pathFor    func(T) []string
	pathForKey func(K) []string
}

func (r *memoryResource[T, K]) Get(ctx context.Context, k K) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := path.Join(r.pathForKey(k)...)
	v, ok := r.data[p]
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}

func (r *memoryResource[T, K]) Insert(ctx context.Context, v T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := path.Join(r.pathFor(v)...)
	if _, ok := r.data[p]; ok {
		return ErrAlreadyExists
	}
	r.data[p] = v
	return nil
}

func (r *memoryResource[T, K]) Update(ctx context.Context, v T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := path.Join(r.pathFor(v)...)
	if _, ok := r.data[p]; !ok {
		return ErrNotFound
	}
	r.data[p] = v
	return nil
}

func (r *memoryResource[T, K]) Upsert(ctx context.Context, v T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := path.Join(r.pathFor(v)...)
	r.data[p] = v
	return nil
}

func (r *memoryResource[T, K]) Mutate(ctx context.Context, k K, fn func(T) (T, error)) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := path.Join(r.pathForKey(k)...)
	cur, ok := r.data[p]
	if !ok {
		var zero T
		return zero, ErrNotFound
	}
	next, err := fn(cur)
	if err != nil {
		return cur, err
	}
	r.data[p] = next
	return next, nil
}
