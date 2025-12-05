// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
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
		dirHint      string
		repoYAML     string
		expectedReqs []string
		expectedDir  string
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
			expectedDir:  "",
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
			expectedDir:  "",
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
			expectedDir:  "sub2",
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
			expectedDir:  "sub3",
		},
		{
			name:    "pyproject.toml - Detect package with dir hint",
			pkg:     "something-else", // Intentionally set to match the other pyproject.toml file.
			version: "1.2.3",          //   Making sure the hint overrides it.
			dirHint: "sub4",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub4/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
      sub3/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=59.0.0"]
        build-backend = "setuptools.build_meta"
        
        [project]
        name = "something-else"
        version = "1.2.3"
`,
			expectedReqs: []string{"setuptools>=61.0.0"},
			expectedDir:  "sub4",
		},
		{
			name:    "setup.cfg - Parse a cfg with a single entry setup_requires",
			pkg:     "single-cfg-package",
			version: "1.7.2",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [metadata]
        name = single-cfg-package
        version = 1.7.2
        
        [options]
        setup_requires = setuptools_scm
`,
			expectedReqs: []string{"setuptools_scm"},
		},
		{
			name:    "setup.cfg - Parse a cfg with a semi-colon seperated setup_requires",
			pkg:     "semi-cfg-package",
			version: "1.4.5",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [metadata]
        name = semi-cfg-package
        version = 1.4.5
        
        [options]
        setup_requires = setuptools; setuptools_scm[toml]
`,
			expectedReqs: []string{"setuptools", "setuptools_scm[toml]"},
		},
		{
			name:    "setup.cfg - Parse a cfg with a dangling list",
			pkg:     "hard-cfg-package",
			version: "1.2",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [metadata]
        name = hard-cfg-package
        version = 1.2
        
        [options]
        setup_requires =
            setuptools
            wheel
            pytest-runner
`,
			expectedReqs: []string{"setuptools", "wheel", "pytest-runner"},
		},
		{
			name:    "setup.cfg with pyproject- Parse the correct cfg with a dangling list using the pyproject file",
			pkg:     "hard-cfg-pyproject-package",
			version: "5.7.3",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [metadata]
        name = hard-cfg-package
        version = 1.2
        
        [options]
        setup_requires =
            setuptools
            wheel
            pytest-runner
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
      sub1/setup.cfg: |
        [options]
        setup_requires = setuptools_scm
      sub1/pyproject.toml: |
        [project]
        name = "hard-cfg-pyproject-package"
        version = "5.7.3"
`,
			expectedReqs: []string{"setuptools_scm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setup the commit tree using repo yaml
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())

			reqs, dir, err := ExtractAllRequirements(ctx, tree, tc.pkg, tc.version, tc.dirHint)
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

			if dir != tc.expectedDir {
				t.Fatalf("Unexpected directory extracted. Wanted: %q, Got: %q", tc.expectedDir, dir)
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
