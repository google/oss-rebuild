// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDockerRunExecutorUploadFileRejectsSymlink(t *testing.T) {
	assertUploadFileRejectsSymlink(t, func(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, filePath string) error {
		return (&DockerRunExecutor{}).uploadFile(ctx, store, asset, filePath)
	})
}

func TestDockerBuildExecutorUploadFileRejectsSymlink(t *testing.T) {
	assertUploadFileRejectsSymlink(t, func(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, filePath string) error {
		return (&DockerBuildExecutor{}).uploadFile(ctx, store, asset, filePath)
	})
}

func assertUploadFileRejectsSymlink(t *testing.T, upload func(context.Context, rebuild.AssetStore, rebuild.Asset, string) error) {
	t.Helper()
	base := t.TempDir()
	outside := filepath.Join(base, "outside-file")
	if err := os.WriteFile(outside, []byte("outside marker"), 0600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "artifact.tgz")
	if err := os.Symlink(outside, src); err != nil {
		t.Fatal(err)
	}

	store := rebuild.NewFilesystemAssetStore(memfs.New())
	asset := rebuild.RebuildAsset.For(rebuild.Target{
		Ecosystem: rebuild.NPM,
		Package:   "test-pkg",
		Version:   "1.0.0",
		Artifact:  "artifact.tgz",
	})
	err := upload(context.Background(), store, asset, src)
	if err == nil {
		t.Fatal("uploadFile succeeded for symlink source, want error")
	}
	if !strings.Contains(err.Error(), "failed to open regular file") {
		t.Fatalf("uploadFile error = %v, want regular file error", err)
	}
	if _, readErr := store.Reader(context.Background(), asset); readErr == nil {
		t.Fatal("asset store contains rejected symlink source")
	}
}
