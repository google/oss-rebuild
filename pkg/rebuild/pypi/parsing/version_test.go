// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

// headTree returns the tree of the single commit in a one-commit repo built
// from repoYAML.
func headTree(t *testing.T, repoYAML string) *object.Tree {
	t.Helper()
	repo := must(gitxtest.CreateRepoFromYAML(repoYAML, nil))
	head := must(repo.Head())
	commit := must(repo.CommitObject(head.Hash()))
	return must(commit.Tree())
}

func TestFindDeclaredVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pkg      string
		dir      string
		repoYAML string
		want     string
	}{
		{
			name: "pyproject project version",
			pkg:  "my-project",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "my-project"
        version = "1.2.3"
`,
			want: "1.2.3",
		},
		{
			name: "pyproject poetry version",
			pkg:  "poetry-pkg",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [tool.poetry]
        name = "poetry-pkg"
        version = "0.9.1"
`,
			want: "0.9.1",
		},
		{
			name: "setup.cfg metadata version",
			pkg:  "cfgpkg",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [metadata]
        name = cfgpkg
        version = 4.5.6
`,
			want: "4.5.6",
		},
		{
			name: "setup.py static version",
			pkg:  "pypkg",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.py: |
        from setuptools import setup
        setup(name="pypkg", version="7.8.9")
`,
			want: "7.8.9",
		},
		{
			name: "setup.py dynamic version unresolved",
			pkg:  "dynpkg",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.py: |
        from setuptools import setup
        setup(name="dynpkg", version=read_version())
`,
			want: "",
		},
		{
			name: "name normalization matches",
			pkg:  "My.Project_Name",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "my-project-name"
        version = "3.0.0"
`,
			want: "3.0.0",
		},
		{
			name: "sibling package name is not matched",
			pkg:  "wanted",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "other"
        version = "1.0.0"
`,
			want: "",
		},
		{
			name: "pyproject wins over setup.py",
			pkg:  "dual",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "dual"
        version = "2.0.0"
      setup.py: |
        from setuptools import setup
        setup(name="dual", version="1.0.0")
`,
			want: "2.0.0",
		},
		{
			name: "monorepo subdirectory",
			pkg:  "sub",
			dir:  "packages/sub",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "root"
        version = "9.9.9"
      packages/sub/pyproject.toml: |
        [project]
        name = "sub"
        version = "1.4.2"
`,
			want: "1.4.2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := headTree(t, tc.repoYAML)
			if got, _ := FindDeclaredVersion(context.Background(), tree, tc.dir, tc.pkg); got != tc.want {
				t.Errorf("FindDeclaredVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindDeclaredVersionDir(t *testing.T) {
	tree := headTree(t, `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "root"
        version = "9.9.9"
      packages/sub/pyproject.toml: |
        [project]
        name = "sub"
        version = "1.4.2"
`)
	for _, tc := range []struct {
		dir, pkg, want, wantDir string
	}{
		{"", "root", "9.9.9", "."},
		{".", "root", "9.9.9", "."},
		{"packages/sub", "sub", "1.4.2", "packages/sub"},
		{"old/sub", "sub", "1.4.2", "packages/sub"}, // stale guess falls back to the tree
		{"old/sub", "nope", "", ""},
	} {
		if got, dir := FindDeclaredVersion(context.Background(), tree, tc.dir, tc.pkg); got != tc.want || dir != tc.wantDir {
			t.Errorf("FindDeclaredVersion(%q, %q) = %q, %q, want %q, %q", tc.dir, tc.pkg, got, dir, tc.want, tc.wantDir)
		}
	}
}
