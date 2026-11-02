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
		wantUses bool
	}{
		{
			name: "ActionTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: actions/checkout@v4
				      - uses: pypa/cibuildwheel@v2.16.2`,
			want:     []string{"2.16.2"},
			wantUses: true,
		},
		{
			name: "ActionMinorTag",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@v2.16`,
			want:     []string{"2.16"},
			wantUses: true,
		},
		{
			name: "ActionCommit",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@` + testCommit("5") + ` # v2.16.2`,
			want:     []string{testCommit("5")},
			wantUses: true,
		},
		{
			name: "ActionBranchOrMajorOrPreReleaseTagIgnoredForSpecs",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - uses: pypa/cibuildwheel@main
				      - uses: pypa/cibuildwheel@release/2.16
				      - uses: pypa/cibuildwheel@v2
				      - uses: pypa/cibuildwheel@v2.16.0b1`,
			want:     nil,
			wantUses: true,
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
			want:     []string{"3.1.0", "2.16.2", "1.12.0"},
			wantUses: true,
		},
		{
			name: "RangeAndPreReleaseIgnoredForSpecs",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: |
				          pip install "cibuildwheel>=2.16,<3"
				          pip install cibuildwheel==3.0.0b1`,
			want:     nil,
			wantUses: true,
		},
		{
			name: "UnpinnedCommand",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: python -m cibuildwheel --output-dir wheelhouse`,
			want:     nil,
			wantUses: true,
		},
		{
			name: "SimilarName",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      - run: pip install cibuildwheel_fork`,
			want:     nil,
			wantUses: false,
		},
		{
			name: "CommentedOut",
			workflow: `
				jobs:
				  wheels:
				    steps:
				      # - uses: pypa/cibuildwheel@v2.16.2
				      - uses: actions/checkout@v4`,
			want:     nil,
			wantUses: false,
		},
		{
			name:     "InvalidYAML",
			workflow: `jobs: [`,
			want:     nil,
			wantUses: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotUses := cibuildwheelSpecs([]byte(textwrap.Dedent(tc.workflow)))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cibuildwheelSpecs() specs diff (-want +got):\n%s", diff)
			}
			if gotUses != tc.wantUses {
				t.Errorf("cibuildwheelSpecs() uses = %t, want %t", gotUses, tc.wantUses)
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
		name     string
		files    gitxtest.FileContent
		want     []string
		wantUses bool
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
			want:     []string{"2.3.1", "2.16.2"},
			wantUses: true,
		},
		{
			name: "UnpinnedSecondWorkflow",
			files: gitxtest.FileContent{
				".github/workflows/test.yml": textwrap.Dedent(`
					jobs:
					  test:
					    steps:
					      - run: pytest`),
				".github/workflows/wheels.yml": textwrap.Dedent(`
					jobs:
					  wheels:
					    steps:
					      - run: pipx run cibuildwheel`),
			},
			want:     nil,
			wantUses: true,
		},
		{
			name:     "NoWorkflows",
			files:    gitxtest.FileContent{"README.md": "# test-package\n"},
			want:     nil,
			wantUses: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			got, gotUses, err := extractCibuildwheelSpecs(must(commit.Tree()))
			if err != nil {
				t.Fatalf("extractCibuildwheelSpecs() returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelSpecs() specs diff (-want +got):\n%s", diff)
			}
			if gotUses != tc.wantUses {
				t.Errorf("extractCibuildwheelSpecs() uses = %t, want %t", gotUses, tc.wantUses)
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
		tags      WheelTags
		libc      platform.LibcImpl
		files     gitxtest.FileContent
		want      []string
	}{
		{
			name:  "TopLevelAndLinuxHooksGlibc",
			tags:  WheelTags{Python: "cp310", ABI: "cp310"},
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"pyproject.toml": hooks},
			want:  []string{"libffi-devel", "zlib-devel"},
		},
		{
			name:  "TopLevelAndLinuxHooksMusl",
			tags:  WheelTags{Python: "cp310", ABI: "cp310"},
			libc:  platform.Musl,
			files: gitxtest.FileContent{"pyproject.toml": hooks},
			want:  []string{"graphviz-dev"},
		},
		{
			name: "OverridesFilteringManylinuxTarget",
			tags: WheelTags{Python: "cp310", ABI: "abi3"},
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[[tool.cibuildwheel.overrides]]
					select = "*-macosx* *-manylinux*"
					before-all = "dnf install -y gd-devel cairo-devel"
					[[tool.cibuildwheel.overrides]]
					select = "cp310-manylinux_x86_64"
					before-all = "dnf install -y pango-devel"
					[[tool.cibuildwheel.overrides]]
					select = "cp{39,310,311}-manylinux*"
					before-all = "dnf install -y brace-devel"
					[[tool.cibuildwheel.overrides]]
					select = "cp31[!1]-manylinux*"
					before-all = "dnf install -y negated-class-devel"
					[[tool.cibuildwheel.overrides]]
					select = "cp31[!0]-manylinux*"
					before-all = "dnf install -y negated-current-python-ignored"
					[[tool.cibuildwheel.overrides]]
					select = "*-manylinux_{aarch64,ppc64le}"
					before-all = "dnf install -y brace-other-arch-ignored"
					[[tool.cibuildwheel.overrides]]
					select = "   "
					before-all = "dnf install -y blank-select-ignored"
					[[tool.cibuildwheel.overrides]]
					select = "pp* cp36-* cp313t-*"
					before-all = "dnf install -y wrong-python-should-be-skipped"
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
			want: []string{"gd-devel", "cairo-devel", "pango-devel", "brace-devel", "negated-class-devel"},
		},
		{
			name: "OverridesFilteringFreeThreadedTarget",
			tags: WheelTags{Python: "cp313", ABI: "cp313t"},
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[[tool.cibuildwheel.overrides]]
					select = "cp313t-manylinux_*"
					before-all = "dnf install -y freethreaded-devel"
					[[tool.cibuildwheel.overrides]]
					select = "cp313-manylinux_*"
					before-all = "dnf install -y gil-only-devel"`),
			},
			want: []string{"freethreaded-devel"},
		},
		{
			name: "OverridesFilteringMusllinuxTarget",
			tags: WheelTags{Python: "cp310", ABI: "cp310"},
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
			tags:      WheelTags{Python: "cp310", ABI: "cp310"},
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
			tags:  WheelTags{Python: "cp310", ABI: "cp310"},
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"setup.py": "from setuptools import setup\nsetup(name='legacy', version='0.1.0')\n"},
			want:  nil,
		},
		{
			name:  "MalformedPyprojectTomlIgnored",
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"pyproject.toml": "[tool.cibuildwheel\nbefore-all = [unterminated\n"},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			got, err := extractCibuildwheelDependencies(must(commit.Tree()), tc.searchDir, tc.tags, tc.libc)
			if err != nil {
				t.Fatalf("extractCibuildwheelDependencies() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelDependencies() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractCibuildwheelEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		searchDir string
		tags      WheelTags
		libc      platform.LibcImpl
		files     gitxtest.FileContent
		want      map[string]string
	}{
		{
			name: "LxmlTableAndOrderedOverridesGlibc",
			tags: WheelTags{Python: "cp312", ABI: "cp312"},
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel]
					environment = {STATIC_DEPS = "true", ZLIB_VERSION = "1.3.2", LIBXML2_VERSION = "2.14.6", LIBXSLT_VERSION = "1.1.43"}
					[[tool.cibuildwheel.overrides]]
					select = "*linux_*"
					environment.CFLAGS = "-O3 -g1 -pipe -fPIC -flto"
					environment.AR = "gcc-ar"
					environment.LDFLAGS = "-fPIC -flto"
					[[tool.cibuildwheel.overrides]]
					select = "*linux_x86_64"
					environment.CFLAGS = "-O3 -g1 -pipe -fPIC -flto -march=core2 -mtune=generic"
					[[tool.cibuildwheel.overrides]]
					select = "*linux_aarch64"
					environment.CFLAGS = "-O3 -g1 -pipe -fPIC -flto -march=armv8-a"`),
			},
			want: map[string]string{
				"AR":              "gcc-ar",
				"CFLAGS":          "-O3 -g1 -pipe -fPIC -flto -march=core2 -mtune=generic",
				"LDFLAGS":         "-fPIC -flto",
				"LIBXML2_VERSION": "2.14.6",
				"LIBXSLT_VERSION": "1.1.43",
				"STATIC_DEPS":     "true",
				"ZLIB_VERSION":    "1.3.2",
			},
		},
		{
			name: "LinuxOverridesTopLevelAndMuslSelectorApplies",
			tags: WheelTags{Python: "cp312", ABI: "cp312"},
			libc: platform.Musl,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel.environment]
					CFLAGS = "-O2"
					DISABLE_AVX2 = 1
					ENABLE_FAST = true
					[tool.cibuildwheel.linux.environment]
					CFLAGS = "-O3 -g0"
					CC = "clang"
					[[tool.cibuildwheel.overrides]]
					select = "*-manylinux*"
					environment = {CYTHON_LIMITED_API = "false"}
					[[tool.cibuildwheel.overrides]]
					select = "*i686 *musllinux* *win32"
					environment = {CYTHON_LIMITED_API = "true"}`),
			},
			want: map[string]string{
				"CC":                 "clang",
				"CFLAGS":             "-O3 -g0",
				"CYTHON_LIMITED_API": "true",
				"DISABLE_AVX2":       "1",
				"ENABLE_FAST":        "true",
			},
		},
		{
			name: "OverridesFilterByWheelTags",
			tags: WheelTags{Python: "cp311", ABI: "cp311"},
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel.environment]
					LIMITED_API = "default"
					[[tool.cibuildwheel.overrides]]
					select = "cp311-*"
					environment = {LIMITED_API = "0", TARGET_PY = "311"}
					[[tool.cibuildwheel.overrides]]
					select = "cp312-*"
					environment = {LIMITED_API = "1", TARGET_PY = "312"}`),
			},
			want: map[string]string{
				"LIMITED_API": "0",
				"TARGET_PY":   "311",
			},
		},
		{
			name: "ShellStringSyntaxAndFilteredEntries",
			tags: WheelTags{Python: "cp312", ABI: "cp312"},
			libc: platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel]
					environment = '''ZSTD_WARNINGS_AS_ERRORS=1 CFLAGS="-O3 -g0" EMPTY_DQ="" EMPTY_SQ='' EXPANDED="$VAR" PATH="$PATH:$HOME/.cargo/bin" CARGO_NET_GIT_FETCH_WITH_CLI="true" VCPKG_ROOT="{project}/vcpkg"'''
					[tool.cibuildwheel.linux.environment]
					PIP_CONSTRAINT = "requirements/cython.txt"
					PIP_EXTRA_INDEX_URL = "https://example.com/simple"
					UV_CONSTRAINT = "requirements/cython.txt"
					LD_LIBRARY_PATH = "/project/.libs"
					PKG_CONFIG_PATH = "/project/"
					AUDITWHEEL_PLAT = "manylinux2014_x86_64"
					RUNNER_OS = "Linux"
					FORCE_COLOR = "1"
					PYTHONUNBUFFERED = "1"
					SKIMAGE_LINK_FLAGS = "-Wl,--strip-debug"
					[[tool.cibuildwheel.overrides]]
					select = "*iphoneos*"
					environment = {FOO_IOS_ONLY = "1"}`),
			},
			want: map[string]string{
				"CFLAGS":                  "-O3 -g0",
				"EMPTY_DQ":                "",
				"EMPTY_SQ":                "",
				"SKIMAGE_LINK_FLAGS":      "-Wl,--strip-debug",
				"ZSTD_WARNINGS_AS_ERRORS": "1",
			},
		},
		{
			name:      "SubdirectoryAndAllFilteredReturnsNil",
			searchDir: "sub",
			tags:      WheelTags{Python: "cp312", ABI: "cp312"},
			libc:      platform.Glibc,
			files: gitxtest.FileContent{
				"pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel.environment]
					STATIC_DEPS = "true"`),
				"sub/pyproject.toml": textwrap.Dedent(`
					[tool.cibuildwheel.environment]
					PIP_CONSTRAINT = "constraints.txt"
					PATH = "$HOME/.cargo/bin:$PATH"`),
			},
			want: nil,
		},
		{
			name:  "MalformedPyprojectReturnsNil",
			libc:  platform.Glibc,
			files: gitxtest.FileContent{"pyproject.toml": "[tool.cibuildwheel\n"},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: tc.files}}, nil))
			commit := must(repo.Repository.CommitObject(git2.Hash(repo.Commits["initial-commit"])))
			got, err := extractCibuildwheelEnvironment(must(commit.Tree()), tc.searchDir, tc.tags, tc.libc)
			if err != nil {
				t.Fatalf("extractCibuildwheelEnvironment() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractCibuildwheelEnvironment() diff (-want +got):\n%s", diff)
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
			want:   []string{"graphviz-dev=14.1.5-r0", "cairo-dev>=1.18"},
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
			script: "yum install -y $EXTRA_PKGS /tmp/custom.rpm ./local.rpm 'bad >= 1.0' '=1.0' 'broken=' 'dup>=<1.0' valid-devel # comment with yum install fake-pkg",
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
