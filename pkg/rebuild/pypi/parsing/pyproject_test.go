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
			name: "UnexpectedHatchHookFieldTypeIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["hatchling"]

        [tool.hatch.build.hooks.custom]
        dependencies = 42
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

func TestExtractHatchWheelHooks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		repoYAML  string
		wantHooks []HatchWheelHook
		wantErr   bool
	}{
		{
			name: "TargetWheelMypycAndCustomHooks",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [project]
        name = "chardet"

        [tool.hatch.build.targets.wheel.hooks.custom]
        enable-by-default = false
        dependencies = ["Cython>=3.0", "setuptools"]

        [tool.hatch.build.targets.wheel.hooks.mypyc]
        enable-by-default = false
        dependencies = ["hatch-mypyc>=0.16.0", "mypy>=1.19"]
        require-runtime-dependencies = true
`,
			wantHooks: []HatchWheelHook{
				{
					Name:            "custom",
					EnableByDefault: false,
					Dependencies:    []string{"Cython>=3.0", "setuptools"},
				},
				{
					Name:                       "mypyc",
					EnableByDefault:            false,
					Dependencies:               []string{"hatch-mypyc>=0.16.0", "mypy>=1.19"},
					RequireRuntimeDependencies: true,
				},
			},
		},
		{
			name: "GlobalHookAndTargetHookOverride",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [tool.hatch.build.hooks.cython]
        enable-by-default = false
        dependencies = ["hatch-cython"]

        [tool.hatch.build.hooks.mypyc]
        enable-by-default = true
        dependencies = ["hatch-mypyc"]

        [tool.hatch.build.targets.wheel.hooks.mypyc]
        enable-by-default = false
        dependencies = ["mypy==1.20.2"]
`,
			wantHooks: []HatchWheelHook{
				{
					Name:            "cython",
					EnableByDefault: false,
					Dependencies:    []string{"hatch-cython"},
				},
				{
					Name:            "mypyc",
					EnableByDefault: false,
					Dependencies:    []string{"mypy==1.20.2"},
				},
			},
		},
		{
			name: "TargetHookOmittingEnableByDefaultDefaultsToTrue",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [tool.hatch.build.hooks.mypyc]
        enable-by-default = false
        dependencies = ["hatch-mypyc"]

        [tool.hatch.build.targets.wheel.hooks.mypyc]
        dependencies = ["mypy==1.20.2"]
`,
			wantHooks: []HatchWheelHook{
				{
					Name:            "mypyc",
					EnableByDefault: true,
					Dependencies:    []string{"mypy==1.20.2"},
				},
			},
		},
		{
			name: "MalformedTOMLIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [tool.hatch.build.hooks.mypyc
        enable-by-default = false
`,
		},
		{
			name: "UnexpectedHookFieldTypeIgnored",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [tool.hatch.build.hooks.custom]
        dependencies = 42
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			tree := must(commit.Tree())
			gotHooks, err := ExtractHatchWheelHooks(tree, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("ExtractHatchWheelHooks() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantHooks, gotHooks, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ExtractHatchWheelHooks() hooks diff (-want +got):\n%s", diff)
			}
		})
	}
}
