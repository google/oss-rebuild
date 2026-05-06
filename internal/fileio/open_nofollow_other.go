// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package fileio

import (
	"os"

	"github.com/pkg/errors"
)

func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Errorf("refusing to open non-regular file %s (mode %s)", path, info.Mode())
	}
	return os.Open(path)
}
