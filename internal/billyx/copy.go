// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package billyx provides utilities for working with billy filesystems.
package billyx

import (
	"io"
	"io/fs"
	"os"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
)

// CopyFS recursively copies all files from src to dst billy.Filesystem.
func CopyFS(dst, src billy.Filesystem) error {
	return util.Walk(src, "/", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == "/" || path == "" {
			return nil
		}
		if info.IsDir() {
			return dst.MkdirAll(path, info.Mode())
		}
		srcFile, err := src.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := dst.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()
		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}
