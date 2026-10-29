// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/msuozzo/go-git2"
	"github.com/msuozzo/go-git2/billyfs"
	"github.com/pkg/errors"
)

// Open opens a go-git2 repository over s's object store. Only
// filesystem-backed storage is supported, unwrapping Storer as needed.
func Open(s storage.Storer) (*git2.Repository, error) {
	if ws, ok := s.(*Storer); ok {
		s = ws.Storer
	}
	fss, ok := s.(*filesystem.Storage)
	if !ok {
		return nil, errors.Errorf("unsupported storer %T", s)
	}
	return git2.Open(billyfs.New(fss.Filesystem()))
}

// Close releases the go-git2 repository, if any.
func (r *Repo) Close() error {
	if r.Repository == nil {
		return nil
	}
	err := r.Repository.Close()
	r.Repository = nil
	return err
}
