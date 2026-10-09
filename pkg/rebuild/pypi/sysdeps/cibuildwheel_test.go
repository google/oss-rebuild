// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func TestExtractCibuildwheelDependencies(t *testing.T) {
	tests := []struct {
		name      string
		searchDir string
		repoYAML  string
		want      []DependencyIdentifier
	}{
		{
			name:      "TopLevelAndLinuxHooks",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [project]
        name = "example"
        version = "1.0.0"

        [tool.cibuildwheel]
        before-all = "yum install -y libffi-devel"
        before-build = ["dnf install -y zlib-devel"]

        [tool.cibuildwheel.linux]
        before-all = "apk add --no-cache graphviz-dev"
`,
			want: []DependencyIdentifier{
				{Namespace: NamespaceYum, Name: "libffi-devel", Provenance: "pyproject.toml:tool.cibuildwheel"},
				{Namespace: NamespaceDnf, Name: "zlib-devel", Provenance: "pyproject.toml:tool.cibuildwheel"},
				{Namespace: NamespaceApk, Name: "graphviz-dev", Provenance: "pyproject.toml:tool.cibuildwheel"},
			},
		},
		{
			name:      "OverridesFilteringLinuxVsNonLinux",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [[tool.cibuildwheel.overrides]]
        select = "*-manylinux*"
        before-all = "dnf install -y gd-devel cairo-devel"

        [[tool.cibuildwheel.overrides]]
        select = "*-musllinux*"
        before-all = "apk add gd-dev cairo-dev"

        [[tool.cibuildwheel.overrides]]
        select = "*-macosx*"
        before-all = "yum install -y should-not-be-extracted"

        [[tool.cibuildwheel.overrides]]
        select = ["*-win_amd64", "*-win32"]
        before-all = "dnf install -y windows-ignored"
`,
			want: []DependencyIdentifier{
				{Namespace: NamespaceDnf, Name: "gd-devel", Provenance: "pyproject.toml:tool.cibuildwheel"},
				{Namespace: NamespaceDnf, Name: "cairo-devel", Provenance: "pyproject.toml:tool.cibuildwheel"},
				{Namespace: NamespaceApk, Name: "gd-dev", Provenance: "pyproject.toml:tool.cibuildwheel"},
				{Namespace: NamespaceApk, Name: "cairo-dev", Provenance: "pyproject.toml:tool.cibuildwheel"},
			},
		},
		{
			name:      "SubdirectorySearch",
			searchDir: "python",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [tool.cibuildwheel.linux]
        before-all = "yum install -y openssl-devel"
      python/pyproject.toml: |
        [project]
        name = "subpkg"
        version = "0.1.0"
        [tool.cibuildwheel]
        before-build = "yum install -y curl-devel"
`,
			want: []DependencyIdentifier{
				{Namespace: NamespaceYum, Name: "curl-devel", Provenance: "python/pyproject.toml:tool.cibuildwheel"},
			},
		},
		{
			name:      "NoPyprojectToml",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup
        setup(name="legacy", version="0.1.0")
`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())
			got, err := ExtractCibuildwheelDependencies(context.Background(), tree, tc.searchDir)
			if err != nil {
				t.Fatalf("ExtractCibuildwheelDependencies() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExtractCibuildwheelDependencies() diff (-want +got):\n%s", diff)
			}
		})
	}
}
