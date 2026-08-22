// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/util"
)

// TestResolverLocalRoundTrip drives a resolved local filesystem the way
// the export jobs do.
func TestResolverLocalRoundTrip(t *testing.T) {
	r := NewResolver()
	fs, path, err := r.FS(t.Context(), filepath.Join(t.TempDir(), "export.txt"))
	if err != nil {
		t.Fatalf("FS: %v", err)
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
	_, path, err := r.FS(t.Context(), "file://"+want)
	if err != nil {
		t.Fatalf("FS: %v", err)
	}
	if path != want {
		t.Errorf("FS path = %q, want %q", path, want)
	}
}

// TestResolverRelativePaths pins the working-directory behavior billy
// chroots would otherwise break.
func TestResolverRelativePaths(t *testing.T) {
	fs, path, err := NewResolver().FS(t.Context(), "out.txt")
	if err != nil {
		t.Fatalf("FS: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path = %q, want absolute", path)
	}
	if fs.Root() != "/" {
		t.Errorf("root = %q, want /", fs.Root())
	}
}

// TestResolverDirFS: a destination URI resolves to a filesystem rooted at
// it, so object names are relative to the destination.
func TestResolverDirFS(t *testing.T) {
	dir := t.TempDir()
	dest, err := NewResolver().DirFS(t.Context(), "file://"+dir)
	if err != nil {
		t.Fatalf("DirFS: %v", err)
	}
	if err := util.WriteFile(dest, "nested/object", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "nested", "object")); err != nil || string(b) != "x" {
		t.Errorf("object under %s = (%q, %v), want x", dir, b, err)
	}
}
