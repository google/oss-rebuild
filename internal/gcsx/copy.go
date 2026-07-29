// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package gcsx extends the GCS client library.
package gcsx

import (
	"context"

	gcs "cloud.google.com/go/storage"
)

// ObjectReader streams an object's content.
type ObjectReader struct {
	*gcs.Reader
	obj *gcs.ObjectHandle
}

// NewObjectReader opens obj for reading.
func NewObjectReader(ctx context.Context, obj *gcs.ObjectHandle) (*ObjectReader, error) {
	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	return &ObjectReader{Reader: r, obj: obj}, nil
}

// ObjectWriter commits content to its object.
type ObjectWriter struct {
	*gcs.Writer
	ctx context.Context
	obj *gcs.ObjectHandle
}

// NewObjectWriter opens obj for writing.
func NewObjectWriter(ctx context.Context, obj *gcs.ObjectHandle) *ObjectWriter {
	return &ObjectWriter{Writer: obj.NewWriter(ctx), ctx: ctx, obj: obj}
}
