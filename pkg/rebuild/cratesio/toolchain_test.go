// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func TestFindPinnedStableToolchain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files gitxtest.FileContent
		dir   string
		want  string
	}{
		{
			name: "plain file",
			files: gitxtest.FileContent{
				"rust-toolchain": "1.84.1\n",
			},
			want: "1.84.1",
		},
		{
			name: "toml file",
			files: gitxtest.FileContent{
				"rust-toolchain.toml": "[toolchain]\nchannel = \"1.84.1\"\n",
			},
			want: "1.84.1",
		},
		{
			name: "nearest file",
			files: gitxtest.FileContent{
				"rust-toolchain":            "1.77.2\n",
				"crates/pkg/rust-toolchain": "1.84.1\n",
			},
			dir:  "crates/pkg",
			want: "1.84.1",
		},
		{
			name: "legacy filename wins",
			files: gitxtest.FileContent{
				"rust-toolchain":      "nightly\n",
				"rust-toolchain.toml": "[toolchain]\nchannel = \"1.84.1\"\n",
			},
		},
		{
			name: "moving stable is left unchanged",
			files: gitxtest.FileContent{
				"rust-toolchain": "stable\n",
			},
		},
		{
			name: "partial release is left unchanged",
			files: gitxtest.FileContent{
				"rust-toolchain": "1.84\n",
			},
		},
		{
			name: "additional toolchain settings are left unchanged",
			files: gitxtest.FileContent{
				"rust-toolchain.toml": "[toolchain]\nchannel = \"1.84.1\"\ncomponents = [\"rustfmt\"]\n",
			},
		},
		{
			name: "additional top-level table is left unchanged",
			files: gitxtest.FileContent{
				"rust-toolchain.toml": "[toolchain]\nchannel = \"1.84.1\"\n\n[metadata]\nowner = \"example\"\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{
				ID:    "target",
				Files: tc.files,
			}}, nil))
			commit := must(repo.CommitObject(repo.Commits["target"]))
			tree := must(commit.Tree())
			got, ok, err := findPinnedStableToolchain(tree, tc.dir)
			if err != nil {
				t.Fatalf("findPinnedStableToolchain: %v", err)
			}
			if got != tc.want || ok != (tc.want != "") {
				t.Errorf("findPinnedStableToolchain() = %q, %v; want %q, %v", got, ok, tc.want, tc.want != "")
			}
		})
	}
}

func TestFindPinnedStableToolchainIgnoresSymlink(t *testing.T) {
	repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{
		ID: "initial",
		Files: gitxtest.FileContent{
			"1.84.1": "ignored\n",
		},
	}}, nil))
	worktree := must(repo.Worktree())
	if err := worktree.Filesystem.Symlink("1.84.1", "rust-toolchain"); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	if _, err := worktree.Add("rust-toolchain"); err != nil {
		t.Fatalf("adding symlink: %v", err)
	}
	hash := must(worktree.Commit("add symlink", &git.CommitOptions{
		Author: &object.Signature{Name: "Test"},
	}))
	commit := must(repo.CommitObject(hash))
	tree := must(commit.Tree())
	if got, ok, err := findPinnedStableToolchain(tree, ""); err != nil {
		t.Fatalf("findPinnedStableToolchain: %v", err)
	} else if got != "" || ok {
		t.Errorf("findPinnedStableToolchain() = %q, %v; want %q, false", got, ok, "")
	}
}
