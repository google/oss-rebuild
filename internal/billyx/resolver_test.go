// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"io"
	"path/filepath"
	"testing"
)

// TestResolverLocalRoundTrip drives a resolved local filesystem the way
// the export jobs do.
func TestResolverLocalRoundTrip(t *testing.T) {
	r := NewResolver()
	fs, path, err := r.ForURI(t.Context(), filepath.Join(t.TempDir(), "export.txt"))
	if err != nil {
		t.Fatalf("ForURI: %v", err)
	}
	w, err := fs.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("round trip")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f, err := fs.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "round trip" {
		t.Errorf("ReadAll = (%q, %v), want %q", b, err, "round trip")
	}
	if r.client != nil {
		t.Error("local resolution dialed GCS")
	}
}

func TestResolverFileScheme(t *testing.T) {
	r := NewResolver()
	want := filepath.Join(t.TempDir(), "export.txt")
	_, path, err := r.ForURI(t.Context(), "file://"+want)
	if err != nil {
		t.Fatalf("ForURI: %v", err)
	}
	if path != want {
		t.Errorf("ForURI path = %q, want %q", path, want)
	}
}

// TestResolverRelativePaths pins the working-directory behavior billy
// chroots would otherwise break.
func TestResolverRelativePaths(t *testing.T) {
	fs, path, err := NewResolver().ForURI(t.Context(), "out.txt")
	if err != nil {
		t.Fatalf("ForURI: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path = %q, want absolute", path)
	}
	if fs.Root() != "/" {
		t.Errorf("root = %q, want /", fs.Root())
	}
}
