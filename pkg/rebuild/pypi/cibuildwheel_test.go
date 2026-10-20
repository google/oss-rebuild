// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/textwrap"
)

func TestCibuildwheelSpecs(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		want     []string
	}{
		{
			name: "ActionTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: actions/checkout@v4
				      - uses: pypa/cibuildwheel@v2.16.2`,
			want: []string{"2.16.2"},
		},
		{
			name: "ActionMinorTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v2.16`,
			want: []string{"2.16"},
		},
		{
			name: "ActionCommit",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@` + testCommit("5") + ` # v2.16.2`,
			want: []string{testCommit("5")},
		},
		{
			name: "ActionBranchOrMajorOrPreReleaseTagIgnored",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@main
				      - uses: pypa/cibuildwheel@release/2.16
				      - uses: pypa/cibuildwheel@v2
				      - uses: pypa/cibuildwheel@v2.16.0b1`,
			want: nil,
		},
		{
			name: "PipAndPipxRequirementsAcrossJobs",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v1.12.0
				  sdist:
				    steps:
				      - run: |
				          python -m pip install "cibuildwheel[uv] == 3.1.0"
				          pipx run cibuildwheel==2.16.2 --output-dir wheelhouse`,
			want: []string{"3.1.0", "2.16.2", "1.12.0"},
		},
		{
			name: "RangeAndPreReleaseIgnored",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: |
				          pip install "cibuildwheel>=2.16,<3"
				          pip install cibuildwheel==3.0.0b1`,
			want: nil,
		},
		{
			name: "CommentedOut",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      # - uses: pypa/cibuildwheel@v2.16.2
				      - uses: actions/checkout@v4`,
			want: nil,
		},
		{
			name:     "InvalidYAML",
			workflow: `jobs: [`,
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cibuildwheelSpecs([]byte(textwrap.Dedent(tc.workflow)))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cibuildwheelSpecs() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractCibuildwheelSpecs(t *testing.T) {
	wheels := textwrap.Dedent(`
		jobs:
		  wheels:
		    steps:
		      - uses: pypa/cibuildwheel@v2.16.2`)
	tests := []struct {
		name  string
		files gitxtest.FileContent
		want  []string
	}{
		{
			name: "AcrossWorkflows",
			files: gitxtest.FileContent{
				".github/workflows/test.yaml": textwrap.Dedent(`
					jobs:
					  test:
					    steps:
					      - run: pip install cibuildwheel==2.3.1`),
				".github/workflows/wheels.yml":         wheels,
				".github/workflows/wheels.yml.bak":     wheels,
				".github/workflows/nested/wheels.yaml": wheels,
			},
			want: []string{"2.3.1", "2.16.2"},
		},
		{
			name:  "NoWorkflows",
			files: gitxtest.FileContent{"README.md": "# test-package\n"},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			got, err := extractCibuildwheelSpecs(must(commit.Tree()))
			if err != nil {
				t.Fatalf("extractCibuildwheelSpecs() returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelSpecs() diff (-want +got):\n%s", diff)
			}
		})
	}
}
