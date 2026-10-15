// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
)

func TestExtractCibuildwheelDependencies(t *testing.T) {
	tests := []struct {
		name      string
		searchDir string
		libc      platform.LibcImpl
		repoYAML  string
		want      []string
	}{
		{
			name:      "TopLevelAndLinuxHooksGlibc",
			searchDir: "",
			libc:      platform.Glibc,
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
			want: []string{"libffi-devel", "zlib-devel"},
		},
		{
			name:      "TopLevelAndLinuxHooksMusl",
			searchDir: "",
			libc:      platform.Musl,
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
			want: []string{"graphviz-dev"},
		},
		{
			name:      "OverridesFilteringManylinuxTarget",
			searchDir: "",
			libc:      platform.Glibc,
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [[tool.cibuildwheel.overrides]]
        select = "*-macosx* *-manylinux*"
        before-all = "dnf install -y gd-devel cairo-devel"

        [[tool.cibuildwheel.overrides]]
        select = "*-musllinux*"
        before-all = "dnf install -y musl-only-should-be-skipped"

        [[tool.cibuildwheel.overrides]]
        select = "*-manylinux_aarch64"
        before-all = "dnf install -y aarch64-only-should-be-skipped"

        [[tool.cibuildwheel.overrides]]
        select = "*-macosx*"
        before-all = "yum install -y should-not-be-extracted"

        [[tool.cibuildwheel.overrides]]
        select = ["*-win_amd64", "*-win32"]
        before-all = "dnf install -y windows-ignored"
`,
			want: []string{"gd-devel", "cairo-devel"},
		},
		{
			name:      "OverridesFilteringMusllinuxTarget",
			searchDir: "",
			libc:      platform.Musl,
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [[tool.cibuildwheel.overrides]]
        select = "*-macosx* *-manylinux*"
        before-all = "apk add manylinux-only-should-be-skipped"

        [[tool.cibuildwheel.overrides]]
        select = "*-musllinux* *-manylinux_i686"
        before-all = "apk add gd-dev cairo-dev"

        [[tool.cibuildwheel.overrides]]
        select = "*-macosx*"
        before-all = "apk add should-not-be-extracted"
`,
			want: []string{"gd-dev", "cairo-dev"},
		},
		{
			name:      "SubdirectorySearch",
			searchDir: "python",
			libc:      platform.Glibc,
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
			want: []string{"curl-devel"},
		},
		{
			name:      "NoPyprojectToml",
			searchDir: "",
			libc:      platform.Glibc,
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
			got, err := extractCibuildwheelDependencies(tree, tc.searchDir, tc.libc)
			if err != nil {
				t.Fatalf("extractCibuildwheelDependencies() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelDependencies() diff (-want +got):\n%s", diff)
			}
		})
	}
}
