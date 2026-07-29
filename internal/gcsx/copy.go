// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package gcsx extends the GCS client library.
package gcsx

import (
	"context"
	"io"

	gcs "cloud.google.com/go/storage"
)

// ObjectReader streams an object's content while enabling server-side copies
// to capable destinations.
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

// WriteTo copies the object server-side when the destination is a copier,
// avoiding streaming the content through this process. It shadows the
// embedded Reader's WriteTo, which io.Copy would otherwise dispatch to.
func (r *ObjectReader) WriteTo(w io.Writer) (int64, error) {
	if d, ok := w.(copier); ok {
		return d.CopyFrom(r.obj)
	}
	return r.Reader.WriteTo(w)
}

// copier is a destination that can ingest a GCS object server-side.
type copier interface {
	CopyFrom(src *gcs.ObjectHandle) (int64, error)
}

// ObjectWriter commits content to its object, streamed or copied server-side
// from another object.
type ObjectWriter struct {
	*gcs.Writer
	ctx    context.Context
	obj    *gcs.ObjectHandle
	copied bool
}

var _ copier = (*ObjectWriter)(nil)

// NewObjectWriter opens obj for writing.
func NewObjectWriter(ctx context.Context, obj *gcs.ObjectHandle) *ObjectWriter {
	return &ObjectWriter{Writer: obj.NewWriter(ctx), ctx: ctx, obj: obj}
}

// CopyFrom copies src's content to the object server-side.
func (w *ObjectWriter) CopyFrom(src *gcs.ObjectHandle) (int64, error) {
	attrs, err := w.obj.CopierFrom(src).Run(w.ctx)
	if err != nil {
		return 0, err
	}
	w.copied = true
	return attrs.Size, nil
}

// Close commits streamed content.
// NOTE: After a server-side copy nothing was streamed so closing the unwritten
// writer would commit an empty object over the copy.
func (w *ObjectWriter) Close() error {
	if w.copied {
		return nil
	}
	return w.Writer.Close()
}
