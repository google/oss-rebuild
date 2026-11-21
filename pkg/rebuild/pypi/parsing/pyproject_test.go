// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/msuozzo/go-git2"
)

func TestExtractPyProjectRequirements(t *testing.T) {
	for _, tc := range []struct {
		name     string
		repoYAML string
		want     []string
		wantErr  bool
	}{
		{
			name: "SurroundingWhitespaceTrimmed",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["  foo  ", "bar >= 1.0  "]
`,
			want: []string{"foo", "bar >= 1.0"},
		},
		{
			// NOTE: Collapsing all whitespace fuses marker keywords like "and" with their operands.
			name: "MarkerWhitespacePreserved",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = [
            "foo ~= 1.0; python_version < '3.14' and platform_python_implementation != 'PyPy'",
            "bar ; sys_platform == 'win32'",
        ]
`,
			want: []string{
				"foo ~= 1.0; python_version < '3.14' and platform_python_implementation != 'PyPy'",
				"bar ; sys_platform == 'win32'",
			},
		},
		{
			name: "EmptyRequires",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = []
`,
			want: nil,
		},
		{
			name: "MissingRequires",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "flit_core.buildapi"
`,
			want: nil,
		},
		{
			name: "MissingBuildSystem",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [project]
        name = "foo"
`,
			want: nil,
		},
		{
			name: "InvalidTOML",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["foo"
`,
			wantErr: true,
		},
		{
			name: "ScikitBuildSetuptoolsScmDottedKey",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["scikit-build-core>=0.11", "numpy~=2.0", "pybind11"]
        build-backend = "scikit_build_core.build"

        [tool.scikit-build]
        metadata.version.provider = "scikit_build_core.metadata.setuptools_scm"
`,
			want: []string{"scikit-build-core>=0.11", "numpy~=2.0", "pybind11", "setuptools-scm"},
		},
		{
			name: "ScikitBuildFancyPyPIReadmeAndSetuptoolsScmSubtables",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["scikit-build-core>=0.10"]
        build-backend = "scikit_build_core.build"

        [tool.scikit-build.metadata.version]
        provider = "scikit_build_core.metadata.setuptools_scm"

        [tool.scikit-build.metadata.readme]
        provider = "scikit_build_core.metadata.fancy_pypi_readme"
`,
			want: []string{"scikit-build-core>=0.10", "hatch-fancy-pypi-readme", "setuptools-scm"},
		},
		{
			name: "ScikitBuildDeduplicatesDeclaredRequirement",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["scikit-build-core>=0.10", "setuptools_scm[toml]>=8"]
        build-backend = "scikit_build_core.build"

        [tool.scikit-build]
        metadata.version.provider = "scikit_build_core.metadata.setuptools_scm"
`,
			want: []string{"scikit-build-core>=0.10", "setuptools_scm[toml]>=8"},
		},
		{
			name: "ScikitBuildUnknownProviderIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["scikit-build-core>=0.10"]
        build-backend = "scikit_build_core.build"

        [tool.scikit-build]
        metadata.version.provider = "local_provider"
`,
			want: []string{"scikit-build-core>=0.10"},
		},
		{
			name: "ScikitBuildMetadataIgnoredForOtherBackend",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["hatchling"]
        build-backend = "hatchling.build"

        [tool.scikit-build]
        metadata.version.provider = "scikit_build_core.metadata.setuptools_scm"
`,
			want: []string{"hatchling"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			tree := must(commit.Tree())
			f := must(tree.File("pyproject.toml"))
			got, err := extractPyProjectRequirements(context.Background(), f)
			if (err != nil) != tc.wantErr {
				t.Fatalf("extractPyProjectRequirements() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("extractPyProjectRequirements() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequirementName(t *testing.T) {
	tests := []struct {
		req  string
		want string
	}{
		{"setuptools", "setuptools"},
		{"setuptools<=67.7.2", "setuptools"},
		{"setuptools; python_version != '3.3'", "setuptools"},
		{"setuptools[core]>=61", "setuptools"},
		{"flit_core==3.7.1", "flit-core"},
		{"Flit.Core==3.7.1", "flit-core"},
		{"setuptools-scm<6.0", "setuptools-scm"},
		{"numpy (>=1.13)", "numpy"},
		{"numpy(>=1.13)", "numpy"},
		{"pip @ https://example.com/pip.zip", "pip"},
		{"pip@https://example.com/pip.zip", "pip"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := RequirementName(tt.req); got != tt.want {
			t.Errorf("RequirementName(%q) = %q, want %q", tt.req, got, tt.want)
		}
	}
}
