// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/msuozzo/go-git2"
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
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
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

func TestExtractCibuildwheelDependencies(t *testing.T) {
	hooks := textwrap.Dedent(`
		[tool.cibuildwheel]
		before-all = "yum install -y libffi-devel"
		before-build = ["dnf install -y zlib-devel"]
		[tool.cibuildwheel.linux]
		before-all = "apk add --no-cache graphviz-dev"`)
	tests := []struct {
		name      string
		searchDir string
		libc      platform.LibcImpl
		files     gitxtest.FileContent
		want      []string
	}{
		{
			name:  "TopLevelAndLinuxHooksGlibc",
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"pyproject.toml": hooks},
			want:  []string{"libffi-devel", "zlib-devel"},
		},
		{
			name:  "TopLevelAndLinuxHooksMusl",
			libc:  platform.Musl,
			files: gitxtest.FileContent{"pyproject.toml": hooks},
			want:  []string{"graphviz-dev"},
		},
		{
			name: "OverridesFilteringManylinuxTarget",
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[[tool.cibuildwheel.overrides]]
					select = "*-macosx* *-manylinux*"
					before-all = "dnf install -y gd-devel cairo-devel"
					[[tool.cibuildwheel.overrides]]
					select = "*-musllinux*"
					before-all = "dnf install -y musl-only-should-be-skipped"
					[[tool.cibuildwheel.overrides]]
					select = "*-manylinux_aarch64"
					before-all = "dnf install -y aarch64-only-should-be-skipped"
					[[tool.cibuildwheel.overrides]]
					select = "*-macosx*"
					before-all = "yum install -y should-not-be-extracted"
					[[tool.cibuildwheel.overrides]]
					select = ["*-win*", "*-android*"]
					before-all = "dnf install -y windows-and-android-ignored"`),
			},
			want: []string{"gd-devel", "cairo-devel"},
		},
		{
			name: "OverridesFilteringMusllinuxTarget",
			libc: platform.Musl,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[[tool.cibuildwheel.overrides]]
					select = "*-macosx* *-manylinux*"
					before-all = "apk add manylinux-only-should-be-skipped"
					[[tool.cibuildwheel.overrides]]
					select = "*-musllinux* *-manylinux_i686"
					before-all = "apk add gd-dev cairo-dev"
					[[tool.cibuildwheel.overrides]]
					select = "*-macosx*"
					before-all = "apk add should-not-be-extracted"`),
			},
			want: []string{"gd-dev", "cairo-dev"},
		},
		{
			name:      "SubdirectorySearch",
			searchDir: "python",
			libc:      platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel.linux]
					before-all = "yum install -y openssl-devel"`),
				"python/pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel]
					before-build = "yum install -y curl-devel"`),
			},
			want: []string{"curl-devel"},
		},
		{
			name:  "NoPyprojectToml",
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"setup.py": "from setuptools import setup\nsetup(name='legacy', version='0.1.0')\n"},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			got, err := extractCibuildwheelDependencies(must(commit.Tree()), tc.searchDir, tc.libc)
			if err != nil {
				t.Fatalf("extractCibuildwheelDependencies() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelDependencies() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParsePackageManagerCommands(t *testing.T) {
	tests := []struct {
		name   string
		script string
		libc   platform.LibcImpl
		want   []string
	}{
		{
			name:   "YumAndMicrodnfWithValueFlags",
			script: "yum -y --enablerepo=epel install --repo epel --disableexcludes all graphviz-devel && microdnf install -y zlib-devel",
			libc:   platform.Glibc,
			want:   []string{"graphviz-devel", "zlib-devel"},
		},
		{
			name: "DnfChainedWithLineContinuationsAndBuildSteps",
			script: `dnf install -y gcc gcc-c++ \
				expat-devel zlib-devel \
				gd-devel cairo-devel pango-devel && \
				curl -L https://example.com/src.tar.gz -o /tmp/src.tar.gz && \
				make install`,
			libc: platform.Glibc,
			want: []string{"gcc", "gcc-c++", "expat-devel", "zlib-devel", "gd-devel", "cairo-devel", "pango-devel"},
		},
		{
			name:   "ApkAddWithVirtualFlagAndVersionPin",
			script: "apk --no-cache add --virtual .build-deps graphviz-dev=14.1.5-r0 'cairo-dev>=1.18' pango-dev@edge !ignored-pkg",
			libc:   platform.Musl,
			want:   []string{"graphviz-dev", "cairo-dev", "pango-dev"},
		},
		{
			name:   "FiltersByTargetLibc",
			script: "yum install -y graphviz-devel || apk add --no-cache graphviz-dev",
			libc:   platform.Musl,
			want:   []string{"graphviz-dev"},
		},
		{
			name:   "InlineIfThenElifConditional",
			script: "if command -v yum; then yum install -y foo-devel; elif command -v apk; then apk add foo-dev; fi",
			libc:   platform.Glibc,
			want:   []string{"foo-devel"},
		},
		{
			name:   "BraceGroupNegationNiceAndBackground",
			script: "{ ! nice -n 10 yum install -y brace-devel; } && yum install -y bg-devel & wait",
			libc:   platform.Glibc,
			want:   []string{"brace-devel", "bg-devel"},
		},
		{
			name:   "SkipsVariablesLocalFilesQuotedSpacesAndComments",
			script: "yum install -y $EXTRA_PKGS /tmp/custom.rpm ./local.rpm 'bad >= 1.0' valid-devel # comment with yum install fake-pkg",
			libc:   platform.Glibc,
			want:   []string{"valid-devel"},
		},
		{
			name:   "SkipsRedirectionsAndTolerantFlag",
			script: "yum -t install -y 2>&1 foo >/dev/null && yum install -y bar &> out.log baz",
			libc:   platform.Glibc,
			want:   []string{"foo", "bar", "baz"},
		},
		{
			name:   "QuotedCommentAndProvides",
			script: `echo "step #1" && dnf install -y "pkgconfig(zlib)" cairo-devel`,
			libc:   platform.Glibc,
			want:   []string{"pkgconfig(zlib)", "cairo-devel"},
		},
		{
			name: "PackageManagerOnlyInCommandPosition",
			script: textwrap.Dedent(`
				echo Skipping yum install echo-devel
				git -C vendor/yum install patch.txt
				sudo -u yum install -m 0644 a b
				CACHE=/var/cache/apk yum install -y foo-devel
				sudo -E env FOO=1 dnf install -y bar-devel`),
			libc: platform.Glibc,
			want: []string{"foo-devel", "bar-devel"},
		},
		{
			name: "CommandSubstitutionAndHeredoc",
			script: textwrap.Dedent(`
				yum install -y $(cat extra.txt) subst-devel
				cat > /tmp/notes <<EOF
				yum install -y heredoc-pkg
				EOF`),
			libc: platform.Glibc,
			want: []string{"subst-devel"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePackageManagerCommands(tc.script, tc.libc)
			if err != nil {
				t.Fatalf("parsePackageManagerCommands() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parsePackageManagerCommands() diff (-want +got):\n%s", diff)
			}
		})
	}
}
