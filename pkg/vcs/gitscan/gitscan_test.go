// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitscan

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/msuozzo/go-git2"
)

func TestExactTreeCountRepeatedBlob(t *testing.T) {
	repo, err := gitxtest.CreateRepoFromYAML(`
commits:
  - id: old
    files:
      a.txt: a
  - id: new
    parent: old
    files:
      a.txt: a
      pkg/__init__.py: ""
      pkg/py.typed: ""
      c.txt: c
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	blob := func(s string) plumbing.Hash { return plumbing.ComputeHash(plumbing.BlobObject, []byte(s)) }
	// An archive repeats the empty blob, as wheels do for __init__.py and py.typed.
	hashes := []plumbing.Hash{blob("a"), blob(""), blob(""), blob("c")}
	closest, matched, total, err := ExactTreeCount{}.Search(context.Background(), repo.GoGit, hashes)
	if err != nil {
		t.Fatal(err)
	}
	if want := repo.Commits["new"].String(); matched != 3 || total != 3 || len(closest) != 1 || closest[0] != want {
		t.Errorf("Search = %v, %d/%d; want [%s], 3/3", closest, matched, total, want)
	}
}

func TestReachableTreeCountUnreachableBlob(t *testing.T) {
	repo, err := gitxtest.CreateRepoFromYAML(`
commits:
  - id: only
    files:
      a.txt: a
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The object store holds a blob that no reachable tree does.
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("dangling")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gitxtest.Open(repo.Storer)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	closest, matched, total, err := ReachableTreeCount{}.Search(context.Background(), r, []git2.Hash{git2.Hash(h)})
	if err != nil {
		t.Fatal(err)
	}
	if len(closest) != 0 || matched != 0 || total != 1 {
		t.Errorf("Search = %v, %d/%d; want no commits, 0/1", closest, matched, total)
	}
}
