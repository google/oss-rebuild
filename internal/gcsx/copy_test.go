// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"io"
	"testing"

	gcs "cloud.google.com/go/storage"
)

type copyRecorder struct {
	src *gcs.ObjectHandle
}

var _ copier = (*copyRecorder)(nil)

func (r *copyRecorder) Write(p []byte) (int, error) { return len(p), nil }

func (r *copyRecorder) CopyFrom(src *gcs.ObjectHandle) (int64, error) {
	r.src = src
	return 1, nil
}

// TestObjectReaderWriteTo proves io.Copy dispatches to the destination's
// server-side copy: a nil embedded Reader makes any streaming attempt panic.
func TestObjectReaderWriteTo(t *testing.T) {
	obj := &gcs.ObjectHandle{}
	rec := &copyRecorder{}
	n, err := io.Copy(rec, &ObjectReader{obj: obj})
	if err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	if n != 1 {
		t.Errorf("io.Copy() = %d, want 1", n)
	}
	if rec.src != obj {
		t.Errorf("copy source = %v, want the reader's object", rec.src)
	}
}

func TestObjectWriterClose(t *testing.T) {
	// A nil embedded Writer makes any commit attempt panic: Close after a
	// server-side copy must not touch it, which would commit an empty object.
	w := &ObjectWriter{copied: true}
	if err := w.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}
