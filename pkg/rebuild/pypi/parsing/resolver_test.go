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

func TestDiscoverBuildDir(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pkg         string
		version     string
		dirHint     string
		repoYAML    string
		expectedDir string
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
        [project]
        name = "my-project"
        version = "1.2.3"
`,
			expectedDir: "",
		},
		{
			name:    "pyproject.toml - For unknown packages, use main pyproject.toml",
			pkg:     "unknown",
			version: "1.2.3",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
`,
			expectedDir: "",
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
        [project]
        name = "something-else"
        version = "1.2.3"
      sub1/pyproject.toml: |
        [project]
        name = "other"
        version = "1.2.3"
      sub2/pyproject.toml: |
        [project]
        name = "pygad"
        version = "3.5.0"
`,
			expectedDir: "sub2",
		},
		{
			name:    "pyproject.toml - Detect poetry packages",
			pkg:     "msteamsapi",
			version: "0.9.5",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub3/pyproject.toml: |
        [tool.poetry]
        name = "msteamsapi"
        version = "0.9.5"
`,
			expectedDir: "sub3",
		},
		{
			name:    "pyproject.toml - Detect package with dir hint",
			pkg:     "something-else",
			version: "1.2.3",
			dirHint: "sub4",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub4/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
      sub3/pyproject.toml: |
        [project]
        name = "something-else"
        version = "1.2.3"
`,
			expectedDir: "sub4",
		},
		{
			name:    "setup.cfg - Basic discovery",
			pkg:     "cfg-package",
			version: "1.7.2",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [metadata]
        name = cfg-package
        version = 1.7.2
`,
			expectedDir: "",
		},
		{
			name:    "setup.cfg with pyproject- Select directory with matching project info",
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
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
      sub1/setup.cfg: |
        [metadata]
        name = hard-cfg-pyproject-package
        version = 5.7.3
`,
			expectedDir: "sub1",
		},
		{
			name:    "setup.py - Basic discovery",
			pkg:     "setup-test",
			version: "1.2",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test',
            version='1.2',
            setup_requires=['pytest-runner'],
        )
`,
			expectedDir: "",
		},
		{
			name:    "setup.py - Select the directory whose name matches",
			pkg:     "setup-test-name",
			version: "1.4.5",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test',
            version='1.2',
            setup_requires=['pytest-runner'],
        )
      sub1/setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test-name',
            version='1.4.5',
            setup_requires=['wheel'],
        )
`,
			expectedDir: "sub1",
		},
		{
			name:    "setup.py - Fall back when the matching name is computed dynamically",
			pkg:     "setup-test-dynamic",
			version: "7.2.8",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub1/setup.py: |
        from setuptools import setup, find_packages
        from os import path

        here = path.abspath(path.dirname(__file__))

        with open(path.join(here, 'VERSION')) as f:
            dyn_version = f.read()

        with open(path.join(here, 'PACKAGE')) as f:
            dyn_name = f.read()

        setup(
            name=dyn_name,
            version=dyn_version,
            setup_requires="pytest-runner",
        )
      sub1/VERSION: |
        7.2.8
      sub1/PACKAGE: |
        setup-test-dynamic
      setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test-name',
            version='1.4.5',
            setup_requires=['wheel'],
        )
`,
			expectedDir: "",
		},
		{
			name:    "All three file types - Select the subdirectory naming the package",
			pkg:     "everything-test",
			version: "4.5.6",
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
      sub1/setup.cfg: |
        [metadata]
        version = 4.5.6

        [options]
        setup_requires =
            setuptools
            wheel
            pytest-runner
      sub1/setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='everything-test',
        )
      sub1/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=59.0.0"]
        build-backend = "setuptools.build_meta"
`,
			expectedDir: "sub1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setup the commit tree using repo yaml
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())
			ctx := context.Background()

			dir, err := DiscoverBuildDir(ctx, tree, tc.pkg, tc.version, tc.dirHint)
			if err != nil {
				t.Fatalf("Failed to discover build dir: %v", err)
			}

			if dir != tc.expectedDir {
				t.Fatalf("Unexpected directory extracted. Wanted: %q, Got: %q", tc.expectedDir, dir)
			}
		})
	}
}

func TestExtractRequirements(t *testing.T) {
	for _, tc := range []struct {
		name         string
		searchDir    string
		repoYAML     string
		expectedReqs []string
	}{
		{
			name:      "pyproject.toml - Standard requires",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0", "wheel"]
`,
			expectedReqs: []string{"setuptools>=61.0.0", "wheel"},
		},
		{
			name:      "pyproject.toml - Poetry requires",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["poetry-core"]
`,
			expectedReqs: []string{"poetry-core"},
		},
		{
			name:      "setup.cfg - Single entry",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [options]
        setup_requires = setuptools_scm
`,
			expectedReqs: []string{"setuptools_scm"},
		},
		{
			name:      "setup.cfg - Multi-line and semi-colon",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.cfg: |
        [options]
        setup_requires =
            setuptools; python_version < "3.7"
            wheel
            pytest-runner
`,
			expectedReqs: []string{"setuptools; python_version < \"3.7\"", "wheel", "pytest-runner"},
		},
		{
			name:      "Both pyproject.toml and setup.cfg",
			searchDir: "sub",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub/pyproject.toml: |
        [build-system]
        requires = ["setuptools"]
      sub/setup.cfg: |
        [options]
        setup_requires = setuptools_scm
`,
			expectedReqs: []string{"setuptools", "setuptools_scm"},
		},
		{
			name:      "Only use searchDir",
			searchDir: "sub1",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["dep1"]
      sub1/pyproject.toml: |
        [build-system]
        requires = ["dep2"]
      sub1/sub1.a/pyproject.toml: |
        [build-system]
        requires = ["dep3"]
      sub2/pyproject.toml: |
        [build-system]
        requires = ["dep4"]
`,
			expectedReqs: []string{"dep2"},
		},
		{
			name:      "setup.py - List setup_requires",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test',
            version='1.2',
            setup_requires=['pytest-runner'],
        )
`,
			expectedReqs: []string{"pytest-runner"},
		},
		{
			name:      "setup.py - String setup_requires",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='setup-test-string',
            version='1.2.3',
            setup_requires="wheel",
        )
`,
			expectedReqs: []string{"wheel"},
		},
		{
			name:      "setup.py - setup_requires referencing a variable",
			searchDir: "",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      setup.py: |
        from setuptools import setup, find_packages

        BUILD_REQUIRES = ['setuptools_scm', 'wheel']

        setup(
            name='setup-test-var',
            version='1.2.3',
            setup_requires=BUILD_REQUIRES,
        )
`,
			expectedReqs: []string{"setuptools_scm", "wheel"},
		},
		{
			name:      "All three file types",
			searchDir: "sub1",
			repoYAML: `
commits:
  - id: initial-commit
    files:
      sub1/pyproject.toml: |
        [build-system]
        requires = ["setuptools>=59.0.0"]
      sub1/setup.cfg: |
        [options]
        setup_requires =
            setuptools
            wheel
      sub1/setup.py: |
        from setuptools import setup, find_packages
        setup(
            name='everything-test',
            setup_requires=['pytest-runner'],
        )
`,
			expectedReqs: []string{"setuptools>=59.0.0", "setuptools", "wheel", "pytest-runner"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setup the commit tree using repo yaml
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commit := must(repo.CommitObject(repo.Commits["initial-commit"]))
			tree := must(commit.Tree())
			ctx := context.Background()

			reqs, err := ExtractRequirements(ctx, tree, tc.searchDir)
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
