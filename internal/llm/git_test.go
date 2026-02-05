// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"slices"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func Test_listRepoFiles(t *testing.T) {
	commits := []gitxtest.Commit{
		{
			Files: gitxtest.FileContent{
				"root.txt":     "root content",
				"dir/file.txt": "file content",
			},
		},
	}
	repo, err := gitxtest.CreateRepo(commits, &gitxtest.RepositoryOptions{
		Storer:   filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	})
	if err != nil {
		t.Fatal(err)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	tree, err := getRepoTree(repo.Repository, head.Hash().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("only immediate children are displayed", func(t *testing.T) {
		files, err := listRepoFiles(tree, "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(files, "root.txt") {
			t.Errorf("immediate file not listed")
		}
		if !slices.Contains(files, "dir/") {
			t.Errorf("immediate subdirectory not listed")
		}
		if slices.Contains(files, "dir/file.txt") {
			t.Errorf("should not include nested file")
		}
	})

	t.Run("listing subdirectories works with and without trailing slash", func(t *testing.T) {
		files, err := listRepoFiles(tree, "dir/")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(files, "file.txt") {
			t.Errorf("file not listed")
		}
		files, err = listRepoFiles(tree, "dir")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(files, "file.txt") {
			t.Errorf("file not listed")
		}
	})
}
