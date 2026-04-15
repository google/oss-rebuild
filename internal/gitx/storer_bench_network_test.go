// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build network

package gitx

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

// BenchmarkOperationsNetwork benchmarks git read operations across storage types.
// This measures the performance impact of storage choice on common git operations
// like iterating commits and reading trees.
func BenchmarkOperationsNetwork(b *testing.B) {
	ctx := context.Background()
	for _, repoInfo := range networkRepos {
		// Define storage configurations to test
		storageConfigs := []struct {
			name  string
			setup func(b *testing.B) (*git.Repository, plumbing.Hash)
		}{
			{
				name: "osfs",
				setup: func(b *testing.B) (*git.Repository, plumbing.Hash) {
					targetDir := b.TempDir()
					fs := osfs.New(targetDir)
					storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
					repo, err := Clone(ctx, storer, nil, &git.CloneOptions{
						URL:           repoInfo.url,
						ReferenceName: repoInfo.ref,
						SingleBranch:  true,
						NoCheckout:    true,
					})
					if err != nil {
						b.Fatalf("Clone failed: %v", err)
					}
					ref, err := repo.Reference(repoInfo.headRef, true)
					if err != nil {
						b.Fatalf("Failed to get ref: %v", err)
					}
					return repo, ref.Hash()
				},
			},
			{
				name: "memfs",
				setup: func(b *testing.B) (*git.Repository, plumbing.Hash) {
					mfs := memfs.New()
					storer := filesystem.NewStorage(mfs, cache.NewObjectLRUDefault())
					repo, err := Clone(ctx, storer, nil, &git.CloneOptions{
						URL:           repoInfo.url,
						ReferenceName: repoInfo.ref,
						SingleBranch:  true,
						NoCheckout:    true,
					})
					if err != nil {
						b.Fatalf("Clone failed: %v", err)
					}
					ref, err := repo.Reference(repoInfo.headRef, true)
					if err != nil {
						b.Fatalf("Failed to get ref: %v", err)
					}
					return repo, ref.Hash()
				},
			},
			{
				name: "memory",
				setup: func(b *testing.B) (*git.Repository, plumbing.Hash) {
					storer := memory.NewStorage()
					repo, err := Clone(ctx, storer, nil, &git.CloneOptions{
						URL:           repoInfo.url,
						ReferenceName: repoInfo.ref,
						SingleBranch:  true,
						NoCheckout:    true,
					})
					if err != nil {
						b.Fatalf("Clone failed: %v", err)
					}
					ref, err := repo.Reference(repoInfo.headRef, true)
					if err != nil {
						b.Fatalf("Failed to get ref: %v", err)
					}
					return repo, ref.Hash()
				},
			},
		}

		for _, sc := range storageConfigs {
			repo, headHash := sc.setup(b)

			b.Run(repoInfo.name+"/"+sc.name+"/iterate_commits", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					commitCount := 0
					iter, _ := repo.Log(&git.LogOptions{From: headHash})
					iter.ForEach(func(c *object.Commit) error {
						commitCount++
						_ = c.Message
						_ = c.Author.Name
						return nil
					})
				}
			})

			b.Run(repoInfo.name+"/"+sc.name+"/read_tree", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					commit, _ := repo.CommitObject(headHash)
					tree, _ := commit.Tree()
					fileCount := 0
					tree.Files().ForEach(func(f *object.File) error {
						fileCount++
						return nil
					})
				}
			})
		}
	}
}
