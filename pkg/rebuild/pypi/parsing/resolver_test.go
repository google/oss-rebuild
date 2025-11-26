// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func TestExtractAllRequirements(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pkg          string
		version      string
		repoYAML     string
		expectedReqs []string
	}{
		{
			name:    "pyproject.toml - Parse the main pyproject.toml",
			pkg:     "my-project",
			version: "1.2.3",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"

        [project]
        name = "my-project"
        version = "1.2.3"
`,
			expectedReqs: []string{"setuptools>=61.0.0"},
		},
		{
			name:    "pyproject.toml - For unknown packages, us main pyproject.toml",
			pkg:     "unknown",
			version: "1.2.3",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
`,
			expectedReqs: []string{"setuptools>=61.0.0"},
		},
		{
			name:    "pyproject.toml - Use the correct subproject for the package",
			pkg:     "pygad",
			version: "3.5.0",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
      sub1/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=59.0.0"]
        build-backend = "setuptools.build_meta"
        
        [project]
        name = "something-else"
        version = "1.2.3"
      sub2/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=42.0.0"]
        build-backend = "setuptools.build_meta"
        
        [project]
        name = "pygad"
        version = "3.5.0"
`,
			expectedReqs: []string{"setuptools>=42.0.0"},
		},
		{
			name:    "pyproject.toml - Detect poetry packages",
			pkg:     "msteamsapi",
			version: "0.9.5",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
      sub3/pyproject.toml: |
        [tool.poetry]
        name = "msteamsapi"
        version = "0.9.5"
        
        [build-system]
        requires = ["poetry-core"]
        build-backend = "poetry.core.masonry.api"
`,
			expectedReqs: []string{"poetry-core"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setup the commit tree using repo yaml
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())
			ctx := context.Background()

			reqs, err := ExtractAllRequirements(ctx, tree, tc.pkg, tc.version)
			if err != nil {
				t.Fatalf("Failed to extract requirements: %v", err)
			}

			diff := make(map[string]int)
			for _, req := range tc.expectedReqs {
				diff[req]++
			}
			for _, req := range reqs {
				diff[req]--
			}
			for _, count := range diff {
				if count != 0 { // If any count is off, print the entire wanted and got slices.
					t.Fatalf("Unexpected requirements extracted.\nWanted: %v\nGot: %v", tc.expectedReqs, reqs)
					break
				}
			}
		})
	}
}
