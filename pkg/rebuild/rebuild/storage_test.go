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
