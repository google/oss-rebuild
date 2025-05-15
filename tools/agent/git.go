// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

func getRepoTree(r *git.Repository, commitHash string) (*object.Tree, error) {
	// Get the commit object
	hash := plumbing.NewHash(commitHash)
	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, errors.Wrap(err, "getting commit object")
	}
	// Get the tree for the commit
	tree, err := commit.Tree()
	if err != nil {
		return nil, errors.Wrap(err, "getting tree for commit")
	}
	return tree, nil
}

func getRepoFile(tree *object.Tree, path string) (string, error) {
	ent, err := tree.FindEntry(path)
	if err != nil {
		return "", err
	}
	if !ent.Mode.IsFile() {
		return "", errors.New("path does not refer to a file")
	}
	f, err := tree.TreeEntryFile(ent)
	if err != nil {
		return "", err
	}
	return f.Contents()
}

func listRepoFiles(tree *object.Tree, path string) ([]string, error) {
	if path == "" {
		path = "."
	}
	var pathTree *object.Tree
	if path != "." {
		ent, err := tree.FindEntry(path)
		if err != nil {
			return nil, err
		}
		if ent.Mode != filemode.Dir {
			return nil, errors.New("path does not refer to a dir")
		}
		pathTree, err = tree.Tree(path)
		if err != nil {
			return nil, err
		}
	} else {
		pathTree = tree
	}
	var names []string
	for _, ent := range pathTree.Entries {
		if ent.Mode.IsFile() {
			names = append(names, ent.Name)
		} else {
			names = append(names, ent.Name+"/")
		}
	}
	return names, nil
}
