// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// NewInMemoryStorer returns git storage backed by an in-memory filesystem,
// tuned for per-request, single-write-then-read-only use: KeepDescriptors
// retains the parsed packfile across object reads and ExclusiveAccess elides
// redundant packfile re-scans, which is safe when using read-only.
// NOTE: KeepDescriptors normally requires calling Close to cleanup but this is
// not required for memfs.
func NewInMemoryStorer() *filesystem.Storage {
	return filesystem.NewStorageWithOptions(memfs.New(), cache.NewObjectLRUDefault(), filesystem.Options{KeepDescriptors: true, ExclusiveAccess: true})
}

// Storer augments go-git's Storer to provide the capability to re-initialize the underlying state.
type Storer struct {
	storage.Storer
	cbk func() storage.Storer
}

// NewStorer creates and initializes a new Storer.
func NewStorer(init func() storage.Storer) *Storer {
	s := &Storer{cbk: init}
	s.Reset()
	return s
}

// Reset recreates the underlying Storer from the callback.
func (s *Storer) Reset() {
	s.Storer = s.cbk()
}
