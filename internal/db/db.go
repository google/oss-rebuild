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
}

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
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
