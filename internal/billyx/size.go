// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"io/fs"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
)

// DirSize returns the total size in bytes of all files under dir, recursively.
func DirSize(bfs billy.Filesystem, dir string) (int64, error) {
	var total int64
	err := util.Walk(bfs, dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
