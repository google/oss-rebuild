// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package fileio

import (
	"os"

	"golang.org/x/sys/unix"
)

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
}
