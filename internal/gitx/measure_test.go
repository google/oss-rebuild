// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

const twoCommitRepo = `
commits:
  - id: first
    branch: master
    message: "first"
    files:
      README.md: "hello"
  - id: second
    parent: first
    branch: master
    message: "second"
    files:
      README.md: "world"
`

func TestObjectStoreSize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		storer  storage.Storer
		wantErr bool
	}{
		{name: "Filesystem", storer: NewInMemoryStorer()},
		{name: "WrappedFilesystem", storer: NewStorer(func() storage.Storer { return NewInMemoryStorer() })},
		{name: "UnsupportedMemory", storer: memory.NewStorage(), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := gitxtest.CreateRepoFromYAML(twoCommitRepo, &gitxtest.RepositoryOptions{Storer: tc.storer, Worktree: memfs.New()})
			if err != nil {
				t.Fatalf("CreateRepoFromYAML: %v", err)
			}
			got, err := ObjectStoreSize(repo.Storer)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("ObjectStoreSize() error = %v; want error %t", err, tc.wantErr)
			}
			if !tc.wantErr && got <= 0 {
				t.Errorf("ObjectStoreSize = %d; want > 0", got)
			}
		})
	}
}

func TestCommitCount(t *testing.T) {
	// The memory-backed default storer shows CommitCount is storer-agnostic.
	repo, err := gitxtest.CreateRepoFromYAML(twoCommitRepo, nil)
	if err != nil {
		t.Fatalf("CreateRepoFromYAML: %v", err)
	}
	got, err := CommitCount(repo.Storer)
	if err != nil {
		t.Fatalf("CommitCount: %v", err)
	}
	if got != 2 {
		t.Errorf("CommitCount = %d; want 2", got)
	}
}
