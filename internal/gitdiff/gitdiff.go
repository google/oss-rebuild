// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitdiff

import (
	"bytes"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/pkg/errors"
)

// Strings computes the header-less unified diff between two strings.
// It uses an in-memory go-git repository to synthetically create
// the patch and then formats only the "hunk" portions.
func Strings(left, right string) (string, error) {
	storer := memory.NewStorage()
	// Create a synthetic git Change object
	fromEntry, err := createChangeEntry(storer, left)
	if err != nil {
		return "", errors.Wrap(err, "creating left entry")
	}
	toEntry, err := createChangeEntry(storer, right)
	if err != nil {
		return "", errors.Wrap(err, "creating right entry")
	}
	change := &object.Change{From: *fromEntry, To: *toEntry}
	// Generate the Patch object for the synthetic Change
	patch, err := object.Changes{change}.Patch()
	if err != nil {
		return "", errors.Wrap(err, "generating patch")
	}
	// Serialize the Patch as a unified diff
	var buf bytes.Buffer
	encoder := diff.NewUnifiedEncoder(&buf, diff.DefaultContextLines)
	if err := encoder.Encode(patch); err != nil {
		return "", errors.Wrap(err, "encoding patch")
	}
	fullDiff := buf.String()
	// Post-process the string to remove the git diff header.
	// The first hunk header will have the prefix @@
	hunkStartIndex := strings.Index(fullDiff, "\n@@")
	if hunkStartIndex == -1 {
		return "", nil // No changes, return empty string
	}
	diff := fullDiff[hunkStartIndex+1:]
	diff = strings.ReplaceAll(diff, "\\ No newline at end of file\n", "")
	if !strings.HasSuffix(diff, "\n") {
		diff = diff + "\n"
	}
	return diff, nil
}

// createChangeEntry creates an object.ChangeEntry for some associated file content.
func createChangeEntry(storer storage.Storer, content string) (*object.ChangeEntry, error) {
	hash, err := storeBlob(storer, content)
	if err != nil {
		return nil, errors.Wrap(err, "failed to store blob")
	}
	entry := object.TreeEntry{Mode: filemode.Regular, Hash: hash}
	treeHash, err := storeTree(storer, &object.Tree{Entries: []object.TreeEntry{entry}})
	if err != nil {
		return nil, errors.Wrap(err, "failed to store tree")
	}
	// Retrieve the "live" Tree object associated with the Storer
	liveTree, err := object.GetTree(storer, treeHash)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get tree")
	}
	return &object.ChangeEntry{Tree: liveTree, TreeEntry: entry}, nil
}

// storeBlob creates and stores a Blob object.
func storeBlob(storer storage.Storer, content string) (plumbing.Hash, error) {
	obj := storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write([]byte(content)); err != nil {
		w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return storer.SetEncodedObject(obj)
}

// storeTree creates and stores a Tree object.
func storeTree(storer storage.Storer, tree *object.Tree) (plumbing.Hash, error) {
	obj := storer.NewEncodedObject()
	obj.SetType(plumbing.TreeObject)
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return storer.SetEncodedObject(obj)
}
