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
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Real-world repos for absolute performance numbers.
// These tests require network access and are opt-in via -tags=network.
var networkRepos = []struct {
	name    string
	url     string
	ref     plumbing.ReferenceName
	headRef plumbing.ReferenceName // Ref to use for operations (tracking ref after clone)
}{
	{"medium_gogit", "https://github.com/go-git/go-git.git", plumbing.Master, "HEAD"},
	{"large_cratesio", "https://github.com/rust-lang/crates.io-index.git", plumbing.Master, "refs/remotes/origin/master"},
}

// BenchmarkCloneNetwork benchmarks clone performance against real GitHub repos.
// Run with: go test -bench=. -tags=network ./internal/gitx/
func BenchmarkCloneNetwork(b *testing.B) {
	if !NativeGitAvailable() {
		b.Skip("native git not available")
	}

	ctx := context.Background()

	for _, repo := range networkRepos {
		b.Run(repo.name+"/native_osfs", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				targetDir := b.TempDir()
				fs := osfs.New(targetDir)
				storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
				_, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})

		b.Run(repo.name+"/native_memfs", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mfs := memfs.New()
				storer := filesystem.NewStorage(mfs, cache.NewObjectLRUDefault())
				_, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})

		b.Run(repo.name+"/native_memory", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				storer := memory.NewStorage()
				_, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})

		b.Run(repo.name+"/gogit_osfs", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				targetDir := b.TempDir()
				fs := osfs.New(targetDir)
				storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
				_, err := git.CloneContext(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})

		b.Run(repo.name+"/gogit_memfs", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				mfs := memfs.New()
				storer := filesystem.NewStorage(mfs, cache.NewObjectLRUDefault())
				_, err := git.CloneContext(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})

		b.Run(repo.name+"/gogit_memory", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				storer := memory.NewStorage()
				_, err := git.CloneContext(ctx, storer, nil, &git.CloneOptions{
					URL:           repo.url,
					ReferenceName: repo.ref,
					SingleBranch:  true,
					NoCheckout:    true,
				})
				if err != nil {
					b.Fatalf("Clone failed: %v", err)
				}
			}
		})
	}
}
