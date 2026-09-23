// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func TestExtractPyProjectRequirements(t *testing.T) {
	for _, tc := range []struct {
		name         string
		toml         string
		expectedReqs []string
		expectErr    bool
	}{
		{
			name: "Zstandard environment markers",
			toml: `
[build-system]
requires = [
    "cffi~=1.17; platform_python_implementation != 'PyPy' and python_version < '3.14'",
    "cffi>=2.0.0b; platform_python_implementation != 'PyPy' and python_version >= '3.14'",
    "packaging",
    "setuptools>=77.0.0",
]
build-backend = "setuptools.build_meta"`,
			expectedReqs: []string{
				"cffi~=1.17; platform_python_implementation != 'PyPy' and python_version < '3.14'",
				"cffi>=2.0.0b; platform_python_implementation != 'PyPy' and python_version >= '3.14'",
				"packaging",
				"setuptools>=77.0.0",
			},
		},
		{
			name: "Spaces around requirements",
			toml: `
[build-system]
requires = ["  dependency1  ", "dependency2 >= 1.0  "]
`,
			expectedReqs: []string{"dependency1", "dependency2 >= 1.0"},
		},
		{
			name: "Complex markers with multiple conditions",
			toml: `
[build-system]
requires = ["defusedxml", "pytest >= 3.1.0; python_version >= '3.5'", "wheel ; sys_platform == 'win32'"]
`,
			expectedReqs: []string{"defusedxml", "pytest >= 3.1.0; python_version >= '3.5'", "wheel ; sys_platform == 'win32'"},
		},
		{
			name: "Invalid toml",
			toml: `
[build-system]
requires = ["abc"
definitely not valid toml
`,
			expectErr: true,
		},
		{
			name: "Empty requires",
			toml: `
[build-system]
requires = []
`,
			expectedReqs: nil,
		},
		{
			name: "Missing requires key",
			toml: `
[build-system]
build-backend = "flit_core.buildapi"
`,
			expectedReqs: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			indentedToml := strings.ReplaceAll(tc.toml, "\n", "\n        ")

			repoYAML := fmt.Sprintf(`
commits:
  - id: initial-commit
    files:
      pyproject.toml: |%s
`, indentedToml)

			repo, err := gitxtest.CreateRepoFromYAML(repoYAML, nil)
			if err != nil {
				t.Fatalf("Failed to create repo: %v", err)
			}
			commit, err := repo.CommitObject(repo.Commits["initial-commit"])
			if err != nil {
				t.Fatalf("Failed to get commit: %v", err)
			}
			tree, err := commit.Tree()
			if err != nil {
				t.Fatalf("Failed to get tree: %v", err)
			}
			f, err := tree.File("pyproject.toml")
			if err != nil {
				t.Fatalf("Failed to get file: %v", err)
			}

			reqs, err := extractPyProjectRequirements(context.Background(), f)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("Expected error but got none")
				}
				return
			} else if err != nil {
				t.Fatalf("Failed to extract requirements: %v", err)
			}

			// normalize nil vs empty slice comparisons
			if len(reqs) == 0 && len(tc.expectedReqs) == 0 {
				return
			}

			if !reflect.DeepEqual(reqs, tc.expectedReqs) {
				t.Fatalf("Unexpected requirements extracted.\nWanted: %q\nGot: %q", tc.expectedReqs, reqs)
			}
		})
	}
}
