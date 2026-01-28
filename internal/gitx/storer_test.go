// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func TestCopyStorer(t *testing.T) {
	// Create a source repository in memory
	yamlRepo := `
commits:
  - id: c1
    branch: master
    message: "First commit"
    tag: v1.0.0
    files:
      file1.txt: "content1"
  - id: c2
    parent: c1
    branch: master
    message: "Second commit"
    files:
      file2.txt: "content2"
  - id: c3
    parent: c1
    branch: feature
    message: "Feature commit"
    files:
      feature.txt: "feature content"
`
	srcStorer := memory.NewStorage()
	srcWT := memfs.New()
	_, err := gitxtest.CreateRepoFromYAML(yamlRepo, &gitxtest.RepositoryOptions{
		Storer:   srcStorer,
		Worktree: srcWT,
	})
	if err != nil {
		t.Fatalf("failed to create source repo: %v", err)
	}

	// Copy to destination
	dstStorer := memory.NewStorage()
	if err := CopyStorer(dstStorer, srcStorer); err != nil {
		t.Fatalf("CopyStorer failed: %v", err)
	}

	// Verify destination has all objects
	srcObjects := 0
	srcIter, _ := srcStorer.IterEncodedObjects(plumbing.AnyObject)
	srcIter.ForEach(func(o plumbing.EncodedObject) error {
		srcObjects++
		// Verify object exists in destination
		_, err := dstStorer.EncodedObject(o.Type(), o.Hash())
		if err != nil {
			t.Errorf("object %s not found in destination: %v", o.Hash(), err)
		}
		return nil
	})

	dstObjects := 0
	dstIter, _ := dstStorer.IterEncodedObjects(plumbing.AnyObject)
	dstIter.ForEach(func(o plumbing.EncodedObject) error {
		dstObjects++
		return nil
	})

	if srcObjects != dstObjects {
		t.Errorf("object count mismatch: src=%d, dst=%d", srcObjects, dstObjects)
	}

	// Verify references are copied
	srcRefs := []string{}
	srcRefIter, _ := srcStorer.IterReferences()
	srcRefIter.ForEach(func(r *plumbing.Reference) error {
		srcRefs = append(srcRefs, r.Name().String())
		return nil
	})

	for _, refName := range srcRefs {
		ref, err := dstStorer.Reference(plumbing.ReferenceName(refName))
		if err != nil {
			t.Errorf("reference %s not found in destination: %v", refName, err)
		} else {
			srcRef, _ := srcStorer.Reference(plumbing.ReferenceName(refName))
			if ref.Hash() != srcRef.Hash() {
				t.Errorf("reference %s hash mismatch: src=%s, dst=%s", refName, srcRef.Hash(), ref.Hash())
			}
		}
	}

	// Verify config is copied
	srcCfg, _ := srcStorer.Config()
	dstCfg, err := dstStorer.Config()
	if err != nil {
		t.Fatalf("failed to get destination config: %v", err)
	}

	// Verify core section is present
	if dstCfg.Core.IsBare != srcCfg.Core.IsBare {
		t.Errorf("config IsBare mismatch: src=%v, dst=%v", srcCfg.Core.IsBare, dstCfg.Core.IsBare)
	}

	// Open repo from destination and verify it works
	dstRepo, err := git.Open(dstStorer, nil)
	if err != nil {
		t.Fatalf("failed to open destination repo: %v", err)
	}

	head, err := dstRepo.Head()
	if err != nil {
		t.Fatalf("failed to get HEAD from destination: %v", err)
	}

	if _, err := dstRepo.CommitObject(head.Hash()); err != nil {
		t.Fatalf("failed to get commit from destination: %v", err)
	}
}
