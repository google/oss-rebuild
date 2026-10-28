// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
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

func TestInTreeBackendModulePaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		repoYAML string
		want     []string
		wantErr  bool
	}{
		{
			name: "ObjectSuffixIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "backend:build_wheel"
        backend-path = ["_build_hook"]
`,
			want: []string{"_build_hook/backend.py", "_build_hook/backend/__init__.py"},
		},
		{
			name: "DottedModuleAcrossBackendPaths",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "pep517_backend.hooks"
        backend-path = ["packaging", "."]
`,
			want: []string{
				"packaging/pep517_backend/hooks.py",
				"packaging/pep517_backend/hooks/__init__.py",
				"pep517_backend/hooks.py",
				"pep517_backend/hooks/__init__.py",
			},
		},
		{
			name: "EscapingAndAbsoluteBackendPathsSkipped",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "backend"
        backend-path = ["../shared", "/abs", "sub/../hooks"]
`,
			want: []string{"hooks/backend.py", "hooks/backend/__init__.py"},
		},
		{
			name: "ScalarBackendPathIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "backend"
        backend-path = "_build_hook"
`,
			want: nil,
		},
		{
			name: "NoBackendPath",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        build-backend = "setuptools.build_meta"
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
        build-backend = "backend
`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())
			f := must(tree.File("pyproject.toml"))
			got, err := InTreeBackendModulePaths(f)
			if (err != nil) != tc.wantErr {
				t.Fatalf("InTreeBackendModulePaths() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("InTreeBackendModulePaths() diff (-want +got):\n%s", diff)
			}
		})
	}
}
