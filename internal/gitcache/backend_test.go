// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalBackendExists(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}
	ctx := context.Background()

	// Non-existent file returns zero time.
	mtime, err := b.exists(ctx, "nofile")
	if err != nil {
		t.Fatalf("exists() error = %v", err)
	}
	if !mtime.IsZero() {
		t.Errorf("exists() mtime = %v, want zero", mtime)
	}

	// Create a file and verify it exists.
	if err := os.WriteFile(filepath.Join(dir, "testfile"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime, err = b.exists(ctx, "testfile")
	if err != nil {
		t.Fatalf("exists() error = %v", err)
	}
	if mtime.IsZero() {
		t.Error("exists() mtime is zero for existing file")
	}
}

func TestLocalBackendWriter(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}
	ctx := context.Background()

	w, err := b.writer(ctx, "sub/dir/file.txt")
	if err != nil {
		t.Fatalf("writer() error = %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sub/dir/file.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file contents = %q, want %q", string(data), "hello")
	}
}

func TestLocalBackendServe(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}

	if err := os.WriteFile(filepath.Join(dir, "cached.tgz"), []byte("tarball-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/get?uri=example", nil)
	rr := httptest.NewRecorder()
	b.serve(rr, req, "cached.tgz")

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.Body.String() != "tarball-data" {
		t.Errorf("body = %q, want %q", rr.Body.String(), "tarball-data")
	}
}

func TestLocalBackendDelete(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}
	ctx := context.Background()

	// Delete non-existent file should succeed.
	if err := b.delete(ctx, "nofile"); err != nil {
		t.Fatalf("delete() of non-existent file error = %v", err)
	}

	// Create and then delete.
	path := filepath.Join(dir, "to-delete")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.delete(ctx, "to-delete"); err != nil {
		t.Fatalf("delete() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still exists after delete")
	}
}
