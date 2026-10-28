// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
)

type mockAssetStore struct {
	*FilesystemAssetStore
	readerCalls int
	writerCalls int
}

func newMockAssetStore() *mockAssetStore {
	return &mockAssetStore{
		FilesystemAssetStore: NewFilesystemAssetStore(memfs.New()),
	}
}

func (m *mockAssetStore) Reader(ctx context.Context, a Asset) (io.ReadCloser, error) {
	m.readerCalls++
	return m.FilesystemAssetStore.Reader(ctx, a)
}

func (m *mockAssetStore) Writer(ctx context.Context, a Asset) (io.WriteCloser, error) {
	m.writerCalls++
	return m.FilesystemAssetStore.Writer(ctx, a)
}

func TestCachedAssetStore_Reader_CacheMiss(t *testing.T) {
	ctx := context.Background()
	frontline := newMockAssetStore()
	backline := newMockAssetStore()
	cachedStore := NewCachedAssetStore(frontline, backline)

	asset := Asset{Type: "test", Target: Target{Package: "foo", Version: "1.0"}}
	content := "hello world"

	// Put content in the backline store.
	w, err := backline.FilesystemAssetStore.Writer(ctx, asset)
	if err != nil {
		t.Fatalf("backline.Writer() error = %v", err)
	}
	if _, err := io.Copy(w, strings.NewReader(content)); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	w.Close()

	// First read should be a cache miss.
	r, err := cachedStore.Reader(ctx, asset)
	if err != nil {
		t.Fatalf("cachedStore.Reader() error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	r.Close()

	if string(got) != content {
		t.Errorf("cachedStore.Reader() got = %v, want %v", string(got), content)
	}

	if frontline.readerCalls != 1 {
		t.Errorf("frontline.readerCalls got = %d, want 1", frontline.readerCalls)
	}
	if frontline.writerCalls != 1 {
		t.Errorf("frontline.writerCalls got = %d, want 1", frontline.writerCalls)
	}
	if backline.readerCalls != 1 {
		t.Errorf("backline.readerCalls got = %d, want 1", backline.readerCalls)
	}
}

func TestCachedAssetStore_Reader_CacheHit(t *testing.T) {
	ctx := context.Background()
	frontline := newMockAssetStore()
	backline := newMockAssetStore()
	cachedStore := NewCachedAssetStore(frontline, backline)

	asset := Asset{Type: "test", Target: Target{Package: "foo", Version: "1.0"}}
	content := "hello world"

	// Put content in the frontline store.
	w, err := frontline.FilesystemAssetStore.Writer(ctx, asset)
	if err != nil {
		t.Fatalf("frontline.Writer() error = %v", err)
	}
	if _, err := io.Copy(w, strings.NewReader(content)); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	w.Close()

	// First read should be a cache hit.
	r, err := cachedStore.Reader(ctx, asset)
	if err != nil {
		t.Fatalf("cachedStore.Reader() error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	r.Close()

	if string(got) != content {
		t.Errorf("cachedStore.Reader() got = %v, want %v", string(got), content)
	}

	if frontline.readerCalls != 1 {
		t.Errorf("frontline.readerCalls got = %d, want 1", frontline.readerCalls)
	}
	if frontline.writerCalls != 0 {
		t.Errorf("frontline.writerCalls got = %d, want 0", frontline.writerCalls)
	}
	if backline.readerCalls != 0 {
		t.Errorf("backline.readerCalls got = %d, want 0", backline.readerCalls)
	}
}

func TestCachedAssetStore_URL(t *testing.T) {
	frontline := newMockAssetStore()
	backline := newMockAssetStore()
	cachedStore := NewCachedAssetStore(frontline, backline)

	asset := Asset{Type: "test", Target: Target{Package: "foo", Version: "1.0"}}
	wantURL, _ := url.Parse("file:///foo/1.0/test")

	// Set a specific URL in the mock.
	frontline.FilesystemAssetStore.fs.Create("foo/1.0/test")
	gotURL := cachedStore.URL(asset)

	if gotURL.Path != wantURL.Path {
		t.Errorf("cachedStore.URL() got = %v, want %v", gotURL, wantURL)
	}
}

func TestGCSStore_URL(t *testing.T) {
	s := &GCSStore{bucket: "my-bucket", prefix: "assets", runID: "run1"}
	// PEP 440 epoch. gs:// object names are literal, so the "%21" that
	// url.URL emits for "!" without RawPath would name a different object.
	asset := Asset{Type: "test", Target: Target{Ecosystem: PyPI, Package: "foo", Version: "1!2.0", Artifact: "foo-1!2.0.tar.gz"}}
	want := "gs://my-bucket/assets/pypi/foo/1!2.0/foo-1!2.0.tar.gz/run1/test"
	if got := s.URL(asset).String(); got != want {
		t.Errorf("GCSStore.URL().String() got = %q, want %q", got, want)
	}
}

func TestFilesystemAssetStore_URL(t *testing.T) {
	s := NewFilesystemAssetStore(memfs.New())
	asset := Asset{Type: "test", Target: Target{Ecosystem: PyPI, Package: "foo", Version: "1!2.0", Artifact: "foo-1!2.0.tar.gz"}}
	want := "file:///pypi/foo/1!2.0/foo-1!2.0.tar.gz/test"
	if got := s.URL(asset).String(); got != want {
		t.Errorf("FilesystemAssetStore.URL().String() got = %q, want %q", got, want)
	}
}

func TestCachedReader_Close(t *testing.T) {
	var writeClosed, readClosed bool
	cr := &cachedReader{
		tee:        bytes.NewReader([]byte("")),
		writeClose: func() error { writeClosed = true; return nil },
		readClose:  func() error { readClosed = true; return nil },
	}
	if err := cr.Close(); err != nil {
		t.Fatalf("cr.Close() error = %v", err)
	}
	if !writeClosed {
		t.Error("writeClose was not called")
	}
	if !readClosed {
		t.Error("readClosed was not called")
	}
}

type failingReader struct {
	data []byte
	read bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.read && len(f.data) > 0 {
		f.read = true
		n := copy(p, f.data)
		return n, io.ErrUnexpectedEOF
	}
	return 0, io.ErrUnexpectedEOF
}

type failingReadOnlyStore struct {
	AssetStore
	data []byte
}

func (s *failingReadOnlyStore) Reader(ctx context.Context, a Asset) (io.ReadCloser, error) {
	return io.NopCloser(&failingReader{data: s.data}), nil
}

func TestFilesystemAssetStore_AtomicWriter(t *testing.T) {
	ctx := context.Background()
	fs := memfs.New()
	store := NewFilesystemAssetStore(fs)
	asset := Asset{Type: "test", Target: Target{Ecosystem: "debian", Package: "main/foo", Version: "1.0", Artifact: "foo.deb"}}
	w, err := store.Writer(ctx, asset)
	if err != nil {
		t.Fatalf("Writer() error = %v", err)
	}
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := store.Reader(ctx, asset); err == nil {
		t.Error("expected Reader() to fail before Writer.Close(), but succeeded")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	r, err := store.Reader(ctx, asset)
	if err != nil {
		t.Fatalf("Reader() after Close() error = %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if string(got) != "partial" {
		t.Errorf("got %q, want %q", string(got), "partial")
	}
}

func TestAssetCopy_ErrorCleanup(t *testing.T) {
	ctx := context.Background()
	fs := memfs.New()
	toStore := NewFilesystemAssetStore(fs)
	fromStore := &failingReadOnlyStore{data: []byte("truncated")}
	asset := Asset{Type: "test", Target: Target{Ecosystem: "debian", Package: "main/foo", Version: "1.0", Artifact: "foo.deb"}}
	if err := AssetCopy(ctx, toStore, fromStore, asset); err == nil {
		t.Fatal("expected AssetCopy() to fail, got nil")
	}
	if _, err := toStore.Reader(ctx, asset); err == nil {
		t.Error("expected destination asset not to exist after failed AssetCopy(), but Reader() succeeded")
	}
}

func TestCachedAssetStore_ReadErrorCleanup(t *testing.T) {
	ctx := context.Background()
	frontline := NewFilesystemAssetStore(memfs.New())
	backline := &failingReadOnlyStore{data: []byte("truncated")}
	cached := NewCachedAssetStore(frontline, backline)
	asset := Asset{Type: "test", Target: Target{Ecosystem: "debian", Package: "main/foo", Version: "1.0", Artifact: "foo.deb"}}
	r, err := cached.Reader(ctx, asset)
	if err != nil {
		t.Fatalf("cached.Reader() error = %v", err)
	}
	_, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr == nil {
		t.Fatal("expected io.ReadAll() to fail, got nil")
	}
	if _, err := frontline.Reader(ctx, asset); err == nil {
		t.Error("expected frontline asset not to exist after failed CachedAssetStore read, but Reader() succeeded")
	}
}
