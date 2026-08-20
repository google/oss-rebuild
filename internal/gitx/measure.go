// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/pkg/errors"
)

// ObjectStoreSize returns the size in bytes of s's object store, the packed
// clone plus any loose objects, as held by the storer's filesystem. Only
// filesystem-backed storage is supported, unwrapping Storer as needed.
// Uncompressed size is deliberately not offered since summing it requires
// inflating every object, which costs seconds of CPU on large repos where
// the filesystem stat is near-free.
func ObjectStoreSize(s storage.Storer) (int64, error) {
	if ws, ok := s.(*Storer); ok {
		s = ws.Storer
	}
	fss, ok := s.(*filesystem.Storage)
	if !ok {
		return 0, errors.Errorf("unsupported storer %T", s)
	}
	return billyx.DirSize(fss.Filesystem(), "objects")
}

// CommitCount returns the number of commit objects in s, reachable from a
// ref or not. The typed iteration reads only object headers, never inflating
// payloads.
func CommitCount(s storage.Storer) (commits int64, err error) {
	iter, err := s.IterEncodedObjects(plumbing.CommitObject)
	if err != nil {
		return 0, errors.Wrap(err, "iterating commits")
	}
	err = iter.ForEach(func(plumbing.EncodedObject) error { commits++; return nil })
	iter.Close()
	if err != nil {
		return 0, errors.Wrap(err, "counting commits")
	}
	return commits, nil
}
