// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/pkg/errors"
)

// CopyStorer copies all git data from src to dst storage.
// This includes objects, references, config, and shallow commits.
// NOTE: This is very slow for large repos. If possible, prefer
// copying the underlying metadata content to the underlying destination.
func CopyStorer(dst, src storage.Storer) error {
	// Copy all objects in a single pass
	iter, err := src.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return errors.Wrap(err, "iterating objects")
	}
	err = iter.ForEach(func(obj plumbing.EncodedObject) error {
		_, err := dst.SetEncodedObject(obj)
		return err
	})
	iter.Close()
	if err != nil {
		return errors.Wrap(err, "copying objects")
	}
	// Copy references
	refs, err := src.IterReferences()
	if err != nil {
		return errors.Wrap(err, "iterating references")
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		return dst.SetReference(ref)
	})
	refs.Close()
	if err != nil {
		return errors.Wrap(err, "copying references")
	}
	// Copy config
	cfg, err := src.Config()
	if err != nil {
		return errors.Wrap(err, "reading config")
	}
	if err := dst.SetConfig(cfg); err != nil {
		return errors.Wrap(err, "writing config")
	}
	// Copy shallow commits (for shallow clones)
	shallow, err := src.Shallow()
	if err == nil && len(shallow) > 0 {
		if err := dst.SetShallow(shallow); err != nil {
			return errors.Wrap(err, "writing shallow commits")
		}
	}
	return nil
}
