// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package localfiles

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestButler_Fetch_ZeroByteCacheMiss(t *testing.T) {
	ctx := context.Background()
	fs := memfs.New()
	store := rebuild.NewFilesystemAssetStoreWithRunID(fs, "run1")
	target := rebuild.Target{Ecosystem: rebuild.Debian, Package: "main/7zip", Version: "0:26.03+dfsg-3", Artifact: "7zip_26.03+dfsg-3_amd64.deb"}
	asset := rebuild.RebuildAsset.For(target)
	// Pre-create a 0-byte file directly in the underlying filesystem to simulate a poisoned cache entry.
	f, err := fs.Create(store.URL(asset).Path)
	if err != nil {
		t.Fatalf("fs.Create() error = %v", err)
	}
	f.Close()
	b := NewButler("meta", "logs", "file:///nonexistent", rebuild.RegistryMux{}, func(string) (rebuild.LocatableAssetStore, error) {
		return store, nil
	})
	// A 0-byte file should be treated as a cache miss (falling through to metaAssetstore and failing on missing build info).
	if _, err := b.Fetch(ctx, "run1", asset); err == nil {
		t.Error("expected Fetch() on 0-byte cached file to miss cache and fail, got nil")
	}
	// Writing non-empty content should be treated as a cache hit.
	w, err := store.Writer(ctx, asset)
	if err != nil {
		t.Fatalf("store.Writer() error = %v", err)
	}
	if _, err := w.Write([]byte("deb-contents")); err != nil {
		t.Fatalf("w.Write() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("w.Close() error = %v", err)
	}
	gotPath, err := b.Fetch(ctx, "run1", asset)
	if err != nil {
		t.Fatalf("expected Fetch() on non-empty cached file to hit cache, got error: %v", err)
	}
	if gotPath != store.URL(asset).Path {
		t.Errorf("Fetch() path = %q, want %q", gotPath, store.URL(asset).Path)
	}
}
