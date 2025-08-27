// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package localfiles

import (
	"os"
	"path"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/pkg/errors"
)

const (
	tempRoot = "/tmp/oss-rebuild/" // The directory where all the local files are stored
)

func Rundex() billy.Filesystem {
	return osfs.New(path.Join(tempRoot, layout.RundexDir))
}

func AssetsPath() string {
	return filepath.Join(tempRoot, layout.AssetsDir)
}

func BuildDefs() (*rebuild.FilesystemAssetStore, error) {
	dir := filepath.Join(tempRoot, layout.BuildDefsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create directory %s", dir)
	}
	assetsFS, err := osfs.New("/").Chroot(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
	}
	return rebuild.NewFilesystemAssetStore(assetsFS), nil
}

func AssetStore(runID string) (rebuild.LocatableAssetStore, error) {
	dir := filepath.Join(tempRoot, layout.AssetsDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to create directory %s", dir)
	}
	assetsFS, err := osfs.New("/").Chroot(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to chroot into directory %s", dir)
	}
	return rebuild.NewFilesystemAssetStoreWithRunID(assetsFS, runID), nil
}
