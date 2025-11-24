package parsing

import (
	"context"
	"log"
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

// NOTE: Requires removal of the testFiles/ prefix in utils.go to work properly
// This will be found in a comment line

var ArtificialRepo = []struct {
	name         string
	pkg          string
	version      string
	repoYAML     string
	wantCommitID string
}{
	{
		name:    "PyPIWheelBuild - pyproject parse",
		pkg:     "test-package",
		version: "1.0.0",
		repoYAML: `commits:
  - id: initial-commit
    files:
      pyproject.toml: |
        [build-system]
        requires = ["setuptools>=61.0.0"]
        build-backend = "setuptools.build_meta"
      sub1/pyproject.toml: |
        [build-system]
        # Consider migrating to hatching later: https://hatch.pypa.io/latest/intro/#existing-project
        # requires = ["setuptools>=61.0"]
        # requires = ["setuptools==59.2"]
        requires = ["setuptools"]
        build-backend = "setuptools.build_meta"
        # requires = ["hatchling"]
        # build-backend = "hatchling.build"
        
        [project]
        name = "pygad"
        version = "3.5.0"
        description = "PyGAD: A Python Library for Building the Genetic Algorithm and Training Machine Learning Algoithms (Keras & PyTorch)."
        readme = {file = "README.md", content-type = "text/markdown"}
        requires-python = ">=3"
        license = {file = "LICENSE"}
        authors = [
        {name = "Ahmed Gad", email = "ahmed.f.gad@gmail.com"},
        ]
        maintainers = [
        {name = "Ahmed Gad", email = "ahmed.f.gad@gmail.com"}
        ]
        classifiers = [
        "License :: OSI Approved :: BSD License",
        "Programming Language :: Python",
        "Programming Language :: Python :: 3",
        "Natural Language :: English",
        "Operating System :: OS Independent",
        "Topic :: Scientific/Engineering",
        "Topic :: Scientific/Engineering :: Bio-Informatics",
        "Topic :: Scientific/Engineering :: Artificial Intelligence",
        "Topic :: Software Development",
        "Topic :: Utilities",
        "Intended Audience :: Information Technology",
        "Intended Audience :: Science/Research",
        "Intended Audience :: Developers",
        "Intended Audience :: Education",
        "Intended Audience :: Other Audience"
        ]
        keywords = ["genetic algorithm", "GA", "optimization", "evolutionary algorithm", "natural evolution", "pygad", "machine learning", "deep learning", "neural networks", "tensorflow", "keras", "pytorch"]
        dependencies = [
        "numpy",
        "matplotlib",
        "cloudpickle",
        ]
        
        [project.urls]
        "Homepage" = "https://github.com/ahmedfgad/GeneticAlgorithmPython"
        "Documentation" = "https://pygad.readthedocs.io"
        "GitHub Repository" = "https://github.com/ahmedfgad/GeneticAlgorithmPython"
        "PyPI Project" = "https://pypi.org/project/pygad"
        "Conda Forge Project" = "https://anaconda.org/conda-forge/pygad"
        "Donation Stripe" = "https://donate.stripe.com/eVa5kO866elKgM0144"
        "Donation Open Collective" = "https://opencollective.com/pygad"
        "Donation Paypal" = "http://paypal.me/ahmedfgad"
        
        [project.optional-dependencies]
        deep_learning = ["keras", "torch"]
        
        # PyTest Configuration. Later, PyTest will support the [tool.pytest] table.
        [tool.pytest.ini_options]
        testpaths = ["tests"]
      sub2/pyproject.toml: |
        [project]
        name = "jurigged"
        version = "0.6.1"
        description = "Live update of Python functions"
        authors = [
            { name = "Olivier Breuleux", email = "breuleux@gmail.com" }
        ]
        readme = "README.md"
        license = "MIT"
        requires-python = ">=3.9"
        dependencies = [
            "blessed>=1.20.0",
            "codefind~=0.1.7",
            "ovld>=0.4.0",
            "watchdog>=4.0.2",
        ]
        
        [project.scripts]
        jurigged = "jurigged.live:cli"
        
        [project.urls]
        Homepage = "https://github.com/breuleux/jurigged"
        Repository = "https://github.com/breuleux/jurigged"
        
        [project.optional-dependencies]
        develoop = [
            "giving~=0.4.3",
            "rich>=10.13.0",
        ]
        
        [build-system]
        requires = ["hatchling"]
        build-backend = "hatchling.build"
        
        [tool.uv]
        dev-dependencies = [
            "pytest>=8.3.3",
            "pytest-cov>=5.0.0",
        ]
        
        [tool.ruff]
        line-length = 80
        exclude = ["tests/snippets"]
        
        [tool.ruff.lint]
        extend-select = ["I"]
        ignore = ["E241", "F722", "E501", "E203", "F811", "F821"]
        
        [tool.ruff.lint.isort]
        combine-as-imports = true
        
        [tool.coverage.report]
        exclude_lines = [
            "@abstractmethod",
            "# pragma: no cover"
        ]
        
        [tool.coverage.run]
        omit = [
            "src/jurigged/__main__.py",
            "src/jurigged/runpy.py",
            "src/jurigged/loop/*",
            "tests/*",
        ]
      sub3/pyproject.toml: |
        [tool.poetry]
        name = "msteamsapi"
        version = "0.9.5"
        description = "Microsoft Teams AdaptiveCards API Wrapper for Python 2 and 3"
        authors = ["Alexey Rubasheff <alexey.rubasheff@gmail.com>"]
        readme = "README.md"
        repository = "https://github.com/ALERTua/msteamsapi"
        packages = [
            { include = "msteamsapi" },
        ]
        classifiers = [
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: MIT License",
        "Programming Language :: Python :: 2.7",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
        "Programming Language :: Python :: 3.13",
        "Programming Language :: Python :: 3.14",
        ]
        
        [tool.poetry.dependencies]
        python = ">=2.7.18,<3.0 || >=3.9,<4.0"
        future = { version = "==1.0.0", python = "<3" }
        pathlib = { version = "==1.0.1", python = "<3" }
        enum34 = { version = "==1.1.10", python = "<3" }
        importlib-resources = [
            { version = "^6.4.0", python = ">3" },
            { version = "==3.3.1", python = "<3" }
        ]
        requests = [
            { version = "^2.32.5", python = ">3" },
            { version = "==2.27.1", python = "<3" }
        ]
        
        [tool.poetry.group.dev.dependencies]
        tox = [
            { version = "==4.5.1", python = ">3" },
            { version = "==3.28.0", python = "<3" }
        ]
        python-dotenv = [
            { version = "^1.0.1", python = ">3" },
            { version = "==0.18.0", python = "<3" }
        ]
        pre-commit = [
            { version = "^3.7.1", python = ">3" },
            { version = "==1.21.0", python = "<3" }
        ]
        ruff = { version = "^0.5.4", python = ">3" }
        wheel = { version = "==0.37.1", python = "<3" }
        
        #[[tool.poetry.source]]
        #name = "pypi"
        #priority = "primary"
        #
        #[[tool.poetry.source]]
        #name = "testpypi"
        #url = "https://test.pypi.org/simple/"
        #priority = "explicit"
        
        [build-system]
        requires = ["poetry-core"]
        build-backend = "poetry.core.masonry.api"
`,
		wantCommitID: "initial-commit",
	},
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func TestCorrectSubPyproject(t *testing.T) {
	for _, tc := range ArtificialRepo {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commitHash := repo.Commits["initial-commit"]

			commit, err := repo.CommitObject(commitHash)
			if err != nil {
				t.Fatalf("Failed to load git commit object from artificial: %v", err)
			}
			tree, err := commit.Tree()
			if err != nil {
				t.Fatalf("Failed to load git tree from commit: %v", err)
			}

			reqs, err := ExtractAllRequirements(ctx, tree, "pygad", "3.5.0")
			if err != nil {
				t.Fatalf("Failed to extract requirements: %v", err)
			}

			expectedReqs := []string{"setuptools"}
			if len(reqs) != len(expectedReqs) {
				t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
			}

			for _, expected := range expectedReqs {
				found := false
				for _, req := range reqs {
					if req == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected requirement %s not found in extracted requirements", expected)
				}
			}
		})
	}
}

func TestMainPyproject(t *testing.T) {
	for _, tc := range ArtificialRepo {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commitHash := repo.Commits["initial-commit"]

			commit, err := repo.CommitObject(commitHash)
			if err != nil {
				t.Fatalf("Failed to load git commit object from artificial: %v", err)
			}
			tree, err := commit.Tree()
			if err != nil {
				t.Fatalf("Failed to load git tree from commit: %v", err)
			}

			reqs, err := ExtractAllRequirements(ctx, tree, "unknown", "1.2.3")
			if err != nil {
				t.Fatalf("Failed to extract requirements: %v", err)
			}

			expectedReqs := []string{"setuptools>=61.0.0"}
			if len(reqs) != len(expectedReqs) {
				t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
			}

			for _, expected := range expectedReqs {
				found := false
				for _, req := range reqs {
					if req == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected requirement %s not found in extracted requirements", expected)
				}
			}
		})
	}
}

func TestPoetryPyproject(t *testing.T) {
	for _, tc := range ArtificialRepo {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			commitHash := repo.Commits["initial-commit"]

			commit, err := repo.CommitObject(commitHash)
			if err != nil {
				t.Fatalf("Failed to load git commit object from artificial: %v", err)
			}
			tree, err := commit.Tree()
			if err != nil {
				t.Fatalf("Failed to load git tree from commit: %v", err)
			}

			reqs, err := ExtractAllRequirements(ctx, tree, "msteamsapi", "0.9.5")
			if err != nil {
				t.Fatalf("Failed to extract requirements: %v", err)
			}

			expectedReqs := []string{"poetry-core"}
			if len(reqs) != len(expectedReqs) {
				log.Println(reqs)
				log.Println("---")
				log.Println(expectedReqs)
				log.Println("---")
				t.Fatalf("Expected %d requirements, got %d", len(expectedReqs), len(reqs))
			}

			for _, expected := range expectedReqs {
				found := false
				for _, req := range reqs {
					if req == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected requirement %s not found in extracted requirements", expected)
				}
			}
		})
	}
}
