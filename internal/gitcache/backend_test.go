// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/pkg/errors"
	"google.golang.org/api/option"
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

func TestGCSBackendServe(t *testing.T) {
	// Serve fixed object attrs for any request so serve() reaches the redirect.
	gcs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"bucket": "test-bucket", "generation": "42", "size": "1234"}`)
	}))
	defer gcs.Close()
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(gcs.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	b := &gcsBackend{client: client, bucket: "test-bucket"}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "slashes",
			path: "github.com/org/repo/repo.tgz",
			want: "https://storage.googleapis.com/download/storage/v1/b/test-bucket/o/github.com%2Forg%2Frepo%2Frepo.tgz?alt=media&generation=42",
		},
		{
			name: "space",
			path: "github.com/org/repo/a b/repo.tgz",
			want: "https://storage.googleapis.com/download/storage/v1/b/test-bucket/o/github.com%2Forg%2Frepo%2Fa%20b%2Frepo.tgz?alt=media&generation=42",
		},
		{
			name: "percent",
			path: "github.com/org/repo/100%/repo.tgz",
			want: "https://storage.googleapis.com/download/storage/v1/b/test-bucket/o/github.com%2Forg%2Frepo%2F100%25%2Frepo.tgz?alt=media&generation=42",
		},
		{
			name: "plus",
			path: "github.com/org/repo/v1.0+build/repo.tgz",
			want: "https://storage.googleapis.com/download/storage/v1/b/test-bucket/o/github.com%2Forg%2Frepo%2Fv1.0+build%2Frepo.tgz?alt=media&generation=42",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/get?uri=example", nil)
			rr := httptest.NewRecorder()
			b.serve(rr, req, tc.path)

			if rr.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
			}
			loc := rr.Header().Get("Location")
			if loc != tc.want {
				t.Errorf("Location = %q, want %q", loc, tc.want)
			}
			if got := rr.Header().Get(sizeHeader); got != "1234" {
				t.Errorf("%s = %q, want %q", sizeHeader, got, "1234")
			}

			// Recover the object name the way the JSON API routes it: the
			// rendered path must hold the object as a single escaped segment
			// under /o/ that unescapes to the original name.
			u, err := url.Parse(loc)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", loc, err)
			}
			seg, ok := strings.CutPrefix(u.EscapedPath(), "/download/storage/v1/b/test-bucket/o/")
			if !ok {
				t.Fatalf("path = %q, want prefix %q", u.EscapedPath(), "/download/storage/v1/b/test-bucket/o/")
			}
			if strings.Contains(seg, "/") {
				t.Errorf("object name %q spans multiple path segments", seg)
			}
			if got, err := url.PathUnescape(seg); err != nil || got != tc.path {
				t.Errorf("PathUnescape(%q) = %q, %v, want %q", seg, got, err, tc.path)
			}
		})
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

func TestLocalBackendLease(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}
	ctx := context.Background()
	_, release, err := b.lease(ctx, "org/repo.tgz")
	if err != nil {
		t.Fatalf("lease() error = %v, want held", err)
	}
	if _, _, err := b.lease(ctx, "org/repo.tgz"); !errors.Is(err, errLeaseHeld) {
		t.Fatalf("second lease() error = %v, want errLeaseHeld", err)
	}
	release()
	_, release, err = b.lease(ctx, "org/repo.tgz")
	if err != nil {
		t.Fatalf("lease() after release error = %v, want held", err)
	}
	// A holder that stops renewing loses the lease once it lapses.
	lapsed := time.Now().Add(-leaseTTL - time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "org/repo.tgz"+leaseSuffix), lapsed, lapsed); err != nil {
		t.Fatal(err)
	}
	_, next, err := b.lease(ctx, "org/repo.tgz")
	if err != nil {
		t.Fatalf("lease() over a lapsed lease error = %v, want held", err)
	}
	defer next()
	// The lapsed holder's release leaves its successor's lease in place.
	release()
	if _, _, err := b.lease(ctx, "org/repo.tgz"); !errors.Is(err, errLeaseHeld) {
		t.Errorf("lease() after the lapsed holder's release error = %v, want errLeaseHeld", err)
	}
}

func TestLocalBackendLeaseEndsWhenLost(t *testing.T) {
	defer func(d time.Duration) { leaseRenew = d }(leaseRenew)
	leaseRenew = 10 * time.Millisecond
	dir := t.TempDir()
	b := &localBackend{baseDir: dir}
	held, release, err := b.lease(context.Background(), "repo.tgz")
	if err != nil {
		t.Fatalf("lease() error = %v, want held", err)
	}
	defer release()
	// Another instance took over the claim.
	p := filepath.Join(dir, "repo.tgz"+leaseSuffix)
	if err := os.WriteFile(p, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("held context outlived the lost lease")
	}
	if cause := context.Cause(held); !errors.Is(cause, errLeaseLost) {
		t.Errorf("context.Cause(held) = %v, want errLeaseLost", cause)
	}
	release()
	if b, err := os.ReadFile(p); err != nil || string(b) != "theirs" {
		t.Errorf("successor's lease after release = %q, %v, want it kept", b, err)
	}
}

// fakeLeaseGCS fakes the single-object GCS calls gcsBackend.lease makes.
type fakeLeaseGCS struct {
	mu      sync.Mutex
	gen     int64 // 0 while absent
	lastGen int64
	updated time.Time
}

func (f *fakeLeaseGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"fake"}}`, code)
	}
	switch r.Method {
	case http.MethodPost:
		if f.gen != 0 {
			fail(http.StatusPreconditionFailed)
			return
		}
		f.lastGen++
		f.gen, f.updated = f.lastGen, time.Now()
	case http.MethodGet:
		if f.gen == 0 {
			fail(http.StatusNotFound)
			return
		}
	case http.MethodPatch:
		if r.URL.Query().Get("ifGenerationMatch") != strconv.FormatInt(f.gen, 10) {
			fail(http.StatusPreconditionFailed)
			return
		}
		f.updated = time.Now()
	case http.MethodDelete:
		if r.URL.Query().Get("ifGenerationMatch") != strconv.FormatInt(f.gen, 10) {
			fail(http.StatusPreconditionFailed)
			return
		}
		f.gen = 0
		w.WriteHeader(http.StatusNoContent)
		return
	}
	fmt.Fprintf(w, `{"bucket":"test-bucket","name":"repo.tgz.lease","generation":"%d","updated":%q}`, f.gen, f.updated.Format(time.RFC3339Nano))
}

func newFakeLeaseBackend(t *testing.T) (*gcsBackend, *fakeLeaseGCS) {
	fake := &fakeLeaseGCS{}
	gcs := httptest.NewServer(fake)
	t.Cleanup(gcs.Close)
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(gcs.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return &gcsBackend{client: client, bucket: "test-bucket"}, fake
}

func TestGCSBackendLease(t *testing.T) {
	b, fake := newFakeLeaseBackend(t)
	ctx := context.Background()
	_, release, err := b.lease(ctx, "repo.tgz")
	if err != nil {
		t.Fatalf("lease() error = %v, want held", err)
	}
	if _, _, err := b.lease(ctx, "repo.tgz"); !errors.Is(err, errLeaseHeld) {
		t.Fatalf("second lease() error = %v, want errLeaseHeld", err)
	}
	release()
	if fake.gen != 0 {
		t.Fatalf("lease generation %d still exists after release", fake.gen)
	}
	_, release, err = b.lease(ctx, "repo.tgz")
	if err != nil {
		t.Fatalf("lease() after release error = %v, want held", err)
	}
	defer release()
	// A holder that stops renewing loses the lease once it lapses.
	fake.updated = time.Now().Add(-leaseTTL - time.Minute)
	lapsed := fake.gen
	_, next, err := b.lease(ctx, "repo.tgz")
	if err != nil {
		t.Fatalf("lease() over a lapsed lease error = %v, want held", err)
	}
	defer next()
	if fake.gen == lapsed {
		t.Errorf("lease generation = %d, want a new one replacing the lapsed %d", fake.gen, lapsed)
	}
}

func TestGCSBackendLeaseEndsWhenLost(t *testing.T) {
	defer func(d time.Duration) { leaseRenew = d }(leaseRenew)
	leaseRenew = 10 * time.Millisecond
	b, fake := newFakeLeaseBackend(t)
	held, release, err := b.lease(context.Background(), "repo.tgz")
	if err != nil {
		t.Fatalf("lease() error = %v, want held", err)
	}
	defer release()
	// Another instance replaced the claim with its own.
	fake.mu.Lock()
	fake.lastGen++
	fake.gen = fake.lastGen
	theirs := fake.gen
	fake.mu.Unlock()
	select {
	case <-held.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("held context outlived the lost lease")
	}
	if cause := context.Cause(held); !errors.Is(cause, errLeaseLost) {
		t.Errorf("context.Cause(held) = %v, want errLeaseLost", cause)
	}
	release()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gen != theirs {
		t.Errorf("lease generation after release = %d, want the successor's %d kept", fake.gen, theirs)
	}
}
