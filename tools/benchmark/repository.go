// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package benchmark

import (
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
)

type Repository interface {
	List() ([]string, error)
	Load(string) (PackageSet, error)
}

type fsRepository struct {
	fs billy.Filesystem
}

var _ Repository = (*fsRepository)(nil)

func NewFSRepository(fs billy.Filesystem) *fsRepository {
	return &fsRepository{fs: fs}
}

func (r *fsRepository) List() ([]string, error) {
	all := []string{}
	// The walkFn never returns an error, so util.Walk won't return an error.
	util.Walk(r.fs, ".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Best effort reading, skip failures.
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		all = append(all, path)
		return nil
	})
	return all, nil
}

func (r *fsRepository) Load(path string) (PackageSet, error) {
	return ReadBenchmark(filepath.Join(r.fs.Root(), path))
}
