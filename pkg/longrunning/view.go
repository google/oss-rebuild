// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package longrunning

import (
	"context"
	"errors"

	"github.com/google/oss-rebuild/internal/db"
)

var ErrNotFound = errors.New("operation not found")

// Reader exposes an Operation by ID.
type Reader[R any] interface {
	Get(ctx context.Context, id string) (*Operation[R], error)
}

// Projector converts a domain value into its Operation representation.
type Projector[T, R any] func(T) Operation[R]

// View adapts any db.Resource into a Reader via a Projector and an
// ID↔key codec.
type View[T, K, R any] struct {
	Resource  db.Resource[T, K]
	KeyFor    func(id string) (K, error)
	Projector Projector[T, R]
}

// Get retrieves an Operation by ID from the underlying resource.
func (v *View[T, K, R]) Get(ctx context.Context, id string) (*Operation[R], error) {
	key, err := v.KeyFor(id)
	if err != nil {
		return nil, err
	}
	t, err := v.Resource.Get(ctx, key)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	op := v.Projector(t)
	return &op, nil
}
