// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/textwrap"
)

func TestCibuildwheelVersions(t *testing.T) {
	tests := []struct {
		name     string
		workflow string
		// uploaded only matters for floating tags.
		uploaded time.Time
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
			name: "ActionBranch",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@main`,
			want: nil,
		},
		{
			name: "ActionMinorTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v2.16`,
			uploaded: time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:     []string{"2.16.2"},
		},
		{
			name: "ActionMinorTagBeforeSeries",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v2.16`,
			uploaded: time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:     nil,
		},
		{
			// NOTE: cibuildwheel publishes no major tags.
			name: "ActionMajorTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v2`,
			uploaded: time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:     nil,
		},
		{
			name: "ActionCommit",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@` + testCommit("5") + ` # v2.16.2`,
			want: []string{"2.16.2"},
		},
		{
			name: "UntaggedActionCommit",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@0123456789abcdef0123456789abcdef01234567`,
			want: nil,
		},
		{
			name: "PipRequirement",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: python -m pip install cibuildwheel==2.16.2`,
			want: []string{"2.16.2"},
		},
		{
			name: "PipxRun",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: pipx run cibuildwheel==2.16.2 --output-dir wheelhouse`,
			want: []string{"2.16.2"},
		},
		{
			name: "QuotedRequirementWithExtras",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: python -m pip install "cibuildwheel[uv] == 3.1.0"`,
			want: []string{"3.1.0"},
		},
		{
			name: "RangeRequirement",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: python -m pip install "cibuildwheel>=2.16,<3"`,
			want: nil,
		},
		{
			name: "PreReleaseRequirement",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: python -m pip install cibuildwheel==3.0.0b1`,
			want: nil,
		},
		{
			name: "MultipleVersions",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v1.12.0
				  sdist:
				    steps:
				      - run: |
				          pip install cibuildwheel==2.16.2
				          pipx run cibuildwheel==2.0.0`,
			want: []string{"2.16.2", "2.0.0", "1.12.0"},
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
			got := cibuildwheelVersions([]byte(textwrap.Dedent(tc.workflow)), testPins, tc.uploaded)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cibuildwheelVersions() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractCibuildwheelVersion(t *testing.T) {
	wheels := textwrap.Dedent(`
		jobs:
		  wheels:
		    steps:
		      - uses: pypa/cibuildwheel@v2.16.2`)
	tests := []struct {
		name  string
		files gitxtest.FileContent
		want  string
	}{
		{
			// NOTE: 2.3.1 sorts above 2.16.2 as a string.
			name: "HighestAcrossWorkflows",
			files: gitxtest.FileContent{
				".github/workflows/test.yaml": textwrap.Dedent(`
					jobs:
					  test:
					    steps:
					      - run: pip install cibuildwheel==2.3.1`),
				".github/workflows/wheels.yml": wheels,
			},
			want: "2.16.2",
		},
		{
			name: "IgnoresNonWorkflowFiles",
			files: gitxtest.FileContent{
				".github/workflows/wheels.yml.disabled": wheels,
				".github/workflows/nested/wheels.yml":   wheels,
			},
			want: "",
		},
		{
			name:  "NoWorkflows",
			files: gitxtest.FileContent{"README.md": "# test-package\n"},
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			got, err := extractCibuildwheelVersion(must(commit.Tree()), testPins, time.Time{})
			if err != nil {
				t.Fatalf("extractCibuildwheelVersion() returned error: %v", err)
			}
			if got != tc.want {
				t.Errorf("extractCibuildwheelVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}
