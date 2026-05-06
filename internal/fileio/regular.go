// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package fileio

import (
	"os"

	"github.com/pkg/errors"
)

// OpenRegular opens path for reading only when path names a regular file.
func OpenRegular(path string) (*os.File, error) {
	file, err := openNoFollow(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open regular file %s", path)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, errors.Wrapf(err, "failed to stat regular file %s", path)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.Errorf("refusing to open non-regular file %s (mode %s)", path, info.Mode())
	}
	return file, nil
}
