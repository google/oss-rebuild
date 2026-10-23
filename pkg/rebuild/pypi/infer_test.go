// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
)

func TestInferPythonVersion(t *testing.T) {
	preFix := time.Date(2022, time.January, 1, 0, 0, 0, 0, time.UTC)
	postFix := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		reqs         []string
		registryTime time.Time
		want         string
	}{
		{"pre-fix with no requirements", nil, preFix, "3.11"},
		{"pre-fix unbounded setuptools", []string{"setuptools>=61", "wheel"}, preFix, "3.11"},
		{"pre-fix ceiling above fix", []string{"wheel==0.40.0", "setuptools<=67.7.2"}, preFix, "3.11"},
		{"pre-fix flit", []string{"flit_core==3.9.0", "flit==3.9.0"}, preFix, "3.11"},
		// rsa 4.9: poetry-core 1.0.x stamps "Generator: poetry", so the poetry tool
		// is installed and brings a contemporary setuptools with it.
		{"pre-fix poetry tool", []string{"poetry==1.0.7", "poetry-core>=1.0.0"}, preFix, "3.11"},
		{"post-fix with no requirements", nil, postFix, ""},
		{"post-fix unbounded setuptools", []string{"setuptools>=61", "wheel"}, postFix, ""},
		// requests 2.31.0: a post-fix upload still emitting Platform: UNKNOWN caps
		// setuptools at 57.5.0, which cannot import on 3.12.
		{"post-fix ceiling below fix", []string{"wheel==0.40.0", "setuptools<=57.5.0"}, postFix, "3.11"},
		{"post-fix ceiling above fix", []string{"wheel==0.40.0", "setuptools<=67.7.2"}, postFix, ""},
		{"exclusive bound at fix", []string{"setuptools<66.1.0"}, postFix, "3.11"},
		{"inclusive bound at fix", []string{"setuptools<=66.1.0"}, postFix, ""},
		{"exact pin below fix", []string{"setuptools==58.1.0"}, postFix, "3.11"},
		{"exact pin past fix", []string{"setuptools==70.0.0"}, postFix, ""},
		{"compound constraint", []string{"setuptools>=40.0,<60.0"}, postFix, "3.11"},
		{"case, extras and marker", []string{"SetupTools[core]<60; python_version != '3.3'"}, postFix, "3.11"},
		{"setuptools-scm is not setuptools", []string{"setuptools-scm<6.0"}, postFix, ""},
		{"post-fix flit", []string{"flit_core>=3.2,<4"}, postFix, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferPythonVersion(tt.reqs, tt.registryTime); got != tt.want {
				t.Errorf("inferPythonVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInferRequirements(t *testing.T) {
	bdist := "Wheel-Version: 1.0\nGenerator: bdist_wheel (0.40.0)\nRoot-Is-Purelib: true\nTag: py3-none-any\n"
	modern := "Metadata-Version: 2.1\nName: x\nLicense-File: LICENSE\n"
	tests := []struct {
		name     string
		wheel    string
		metadata string
		want     []string
	}{
		{"bdist_wheel without License-File", bdist, "Metadata-Version: 2.1\nName: x\n", []string{"wheel==0.40.0", "setuptools<=56.2.0"}},
		{"bdist_wheel with Platform UNKNOWN", bdist, modern + "Platform: UNKNOWN\n", []string{"wheel==0.40.0", "setuptools<=57.5.0"}},
		{"bdist_wheel with modern metadata", bdist, modern, []string{"wheel==0.40.0", "setuptools<=67.7.2"}},
		{"setuptools generator pins exactly", "Wheel-Version: 1.0\nGenerator: setuptools (70.0.0)\n", modern, []string{"setuptools==70.0.0"}},
		{"flit generator takes no setuptools", "Wheel-Version: 1.0\nGenerator: flit 3.9.0\n", "Metadata-Version: 2.1\nName: x\n", []string{"flit_core==3.9.0", "flit==3.9.0"}},
		{"pdm-backend generator pins pdm-backend", "Wheel-Version: 1.0\nGenerator: pdm-backend (2.4.4)\n", modern, []string{"pdm-backend==2.4.4"}},
		{"uv generator pins uv-build", "Wheel-Version: 1.0\nGenerator: uv 0.10.0\n", modern, []string{"uv-build==0.10.0"}},
		{"meson generator without version", "Wheel-Version: 1.0\nGenerator: meson\n", modern, []string{}},
		{"meson generator with parenthesized version", "Wheel-Version: 1.0\nGenerator: meson (0.17.1)\n", modern, []string{"meson-python==0.17.1"}},
		{"meson generator with bare version", "Wheel-Version: 1.0\nGenerator: meson 0.17.1\n", modern, []string{"meson-python==0.17.1"}},
		{"scikit-build-core generator with parenthesized version", "Wheel-Version: 1.0\nGenerator: scikit-build-core (0.10.7)\n", modern, []string{"scikit-build-core==0.10.7"}},
		{"scikit-build-core generator with bare version", "Wheel-Version: 1.0\nGenerator: scikit-build-core 0.11.6\n", modern, []string{"scikit-build-core==0.11.6"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zr := wheelZipReader(t, "testpkg", "1.0", tt.wheel, tt.metadata)
			got, err := inferRequirements("testpkg", "1.0", zr)
			if err != nil {
				t.Fatalf("inferRequirements() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("inferRequirements() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequirementName(t *testing.T) {
	tests := []struct {
		req  string
		want string
	}{
		{"setuptools", "setuptools"},
		{"setuptools<=67.7.2", "setuptools"},
		{"setuptools; python_version != '3.3'", "setuptools"},
		{"setuptools[core]>=61", "setuptools"},
		{"flit_core==3.7.1", "flit-core"},
		{"Flit.Core==3.7.1", "flit-core"},
		{"setuptools-scm<6.0", "setuptools-scm"},
		{"numpy (>=1.13)", "numpy"},
		{"numpy(>=1.13)", "numpy"},
		{"pip @ https://example.com/pip.zip", "pip"},
		{"pip@https://example.com/pip.zip", "pip"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := requirementName(tt.req); got != tt.want {
			t.Errorf("requirementName(%q) = %q, want %q", tt.req, got, tt.want)
		}
	}
}

func TestRequirementMarker(t *testing.T) {
	tests := []struct {
		req  string
		want string
	}{
		{"numpy", ""},
		{"numpy==1.14.5", ""},
		{"numpy==1.14.5; python_version>='3.7'", "python_version>='3.7'"},
		{"wheel ; sys_platform == 'win32'", "sys_platform == 'win32'"},
		{"cffi~=1.17; platform_python_implementation != 'PyPy' and python_version < '3.14'", "platform_python_implementation != 'PyPy' and python_version < '3.14'"},
		{"foo; platform_version == '@'", "platform_version == '@'"},
		{"pip @ https://example.com/pip.zip;sha1=abc", ""},
		{"pip @ https://example.com/pip.zip;sha1=abc ; python_version >= '3.8'", "python_version >= '3.8'"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := requirementMarker(tt.req); got != tt.want {
			t.Errorf("requirementMarker(%q) = %q, want %q", tt.req, got, tt.want)
		}
	}
}

func TestGetDistInfoDirAcceptsEquivalentNames(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		version string
		files   []string
		want    string
	}{
		{
			name:    "exact normalized path",
			pkg:     "friendly-bard",
			version: "1.2.3",
			files: []string{
				"friendly_bard-1.2.3.dist-info/WHEEL",
				"friendly_bard-1.2.3.dist-info/METADATA",
			},
			want: "friendly_bard-1.2.3.dist-info",
		},
		{
			name:    "lowercased historical path",
			pkg:     "128Autograder",
			version: "5.2.3",
			files: []string{
				"128autograder-5.2.3.dist-info/WHEEL",
				"128autograder-5.2.3.dist-info/METADATA",
			},
			want: "128autograder-5.2.3.dist-info",
		},
		{
			name:    "dot and hyphen equivalence",
			pkg:     "Friendly.Bard",
			version: "2.0.0",
			files: []string{
				"friendly_bard-2.0.0.dist-info/WHEEL",
				"friendly_bard-2.0.0.dist-info/METADATA",
			},
			want: "friendly_bard-2.0.0.dist-info",
		},
		{
			name:    "historical uppercase hyphenated path",
			pkg:     "friendly-bard",
			version: "3.1.4",
			files: []string{
				"Friendly-Bard-3.1.4.dist-info/WHEEL",
				"Friendly-Bard-3.1.4.dist-info/METADATA",
			},
			want: "Friendly-Bard-3.1.4.dist-info",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zr := testZipReader(t, tt.files)
			got, err := getDistInfoDir(tt.pkg, tt.version, zr)
			if err != nil {
				t.Fatalf("getDistInfoDir() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("getDistInfoDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func testZipReader(t *testing.T, files []string) *zip.Reader {
	t.Helper()

	entries := make([]archive.ZipEntry, 0, len(files))
	for _, name := range files {
		entries = append(entries, archive.ZipEntry{
			FileHeader: &zip.FileHeader{Name: name},
			Body:       []byte("data"),
		})
	}
	buf, err := archivetest.ZipFile(entries)
	if err != nil {
		t.Fatalf("ZipFile(): %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader(): %v", err)
	}
	return zr
}

func TestMergeRequirements(t *testing.T) {
	tests := []struct {
		name      string
		reqs      []string
		buildReqs []string
		want      []string
	}{
		{
			name:      "appends new packages",
			reqs:      []string{"wheel==0.36.2"},
			buildReqs: []string{"setuptools_scm"},
			want:      []string{"wheel==0.36.2", "setuptools_scm"},
		},
		{
			name:      "existing pin wins over build spec",
			reqs:      []string{"setuptools==57.5.0"},
			buildReqs: []string{"setuptools>=40"},
			want:      []string{"setuptools==57.5.0"},
		},
		{
			name:      "repeats within buildReqs collapse",
			reqs:      []string{},
			buildReqs: []string{"setuptools_scm", "setuptools_scm"},
			want:      []string{"setuptools_scm"},
		},
		{
			name:      "empty requirement ignored",
			reqs:      []string{"wheel"},
			buildReqs: []string{"", "  "},
			want:      []string{"wheel"},
		},
		{
			name:      "marker-qualified variant collapses against pin",
			reqs:      []string{"setuptools==57.5.0"},
			buildReqs: []string{"setuptools; python_version != '3.3'"},
			want:      []string{"setuptools==57.5.0"},
		},
		{
			name:      "extras variant collapses against pin",
			reqs:      []string{"cffi==1.15.0"},
			buildReqs: []string{"cffi[dev]"},
			want:      []string{"cffi==1.15.0"},
		},
		{
			name: "marker variants are all kept",
			reqs: []string{},
			buildReqs: []string{
				"wheel",
				"setuptools",
				"Cython>=0.29.2",
				"numpy==1.13.3; python_version=='3.5'",
				"numpy==1.13.3; python_version=='3.6'",
				"numpy==1.14.5; python_version>='3.7'",
			},
			want: []string{
				"wheel",
				"setuptools",
				"Cython>=0.29.2",
				"numpy==1.13.3; python_version=='3.5'",
				"numpy==1.13.3; python_version=='3.6'",
				"numpy==1.14.5; python_version>='3.7'",
			},
		},
		{
			name: "compound marker variants are all kept",
			reqs: []string{"setuptools==77.0.3"},
			buildReqs: []string{
				"cffi~=1.17; platform_python_implementation != 'PyPy' and python_version < '3.14'",
				"cffi>=2.0.0b; platform_python_implementation != 'PyPy' and python_version >= '3.14'",
				"setuptools>=77.0.0",
			},
			want: []string{
				"setuptools==77.0.3",
				"cffi~=1.17; platform_python_implementation != 'PyPy' and python_version < '3.14'",
				"cffi>=2.0.0b; platform_python_implementation != 'PyPy' and python_version >= '3.14'",
			},
		},
		{
			name:      "unconditional and marker variants are both kept",
			reqs:      []string{},
			buildReqs: []string{"numpy>=1.13", "numpy==1.14.5; python_version>='3.7'"},
			want:      []string{"numpy>=1.13", "numpy==1.14.5; python_version>='3.7'"},
		},
		{
			name:      "repeats of one marker collapse",
			reqs:      []string{},
			buildReqs: []string{"numpy; python_version>='3.7'", "numpy>=1.14; python_version>='3.7'"},
			want:      []string{"numpy; python_version>='3.7'"},
		},
		{
			name:      "all marker variants collapse against pin",
			reqs:      []string{"setuptools<=56.2.0"},
			buildReqs: []string{"setuptools<60; python_version<'3.6'", "setuptools>=61; python_version>='3.6'"},
			want:      []string{"setuptools<=56.2.0"},
		},
		{
			name:      "equivalent name spellings collapse",
			reqs:      []string{"flit_core==3.9.0"},
			buildReqs: []string{"Flit-Core>=3.2", "setuptools_scm", "SetupTools-SCM"},
			want:      []string{"flit_core==3.9.0", "setuptools_scm"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeRequirements(tc.reqs, tc.buildReqs)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mergeRequirements() diff (-want +got):\n%s", diff)
			}
		})
	}
}

// wheelZipReader builds a wheel with the given WHEEL and METADATA contents so
// inferRequirements can read the Generator line and metadata heuristics.
func wheelZipReader(t *testing.T, pkg, version, wheel, metadata string) *zip.Reader {
	t.Helper()
	dir := expectedDistInfoDir(pkg, version)
	entries := []archive.ZipEntry{
		{FileHeader: &zip.FileHeader{Name: dir + "/WHEEL"}, Body: []byte(wheel)},
		{FileHeader: &zip.FileHeader{Name: dir + "/METADATA"}, Body: []byte(metadata)},
	}
	buf, err := archivetest.ZipFile(entries)
	if err != nil {
		t.Fatalf("ZipFile(): %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader(): %v", err)
	}
	return zr
}

func TestInferRepoPrefersReleaseLinks(t *testing.T) {
	target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "charset-normalizer", Version: "2.0.12", Artifact: "charset_normalizer-2.0.12-py3-none-any.whl"}
	for _, tc := range []struct {
		name  string
		calls []httpxtest.Call
		want  string
	}{
		{
			name: "release links win over the project's",
			calls: []httpxtest.Call{
				{URL: "https://pypi.org/pypi/charset-normalizer/2.0.12/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","version":"2.0.12","home_page":"https://github.com/ousret/charset_normalizer"}}`)}},
				{URL: "https://pypi.org/pypi/charset-normalizer/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","project_urls":{"Code":"https://github.com/jawah/charset_normalizer"}}}`)}},
			},
			want: "https://github.com/ousret/charset_normalizer",
		},
		{
			name: "a project source link beats a repository cited in the release description",
			calls: []httpxtest.Call{
				{URL: "https://pypi.org/pypi/charset-normalizer/2.0.12/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","version":"2.0.12","description":"builds on https://github.com/psf/requests","project_urls":{"Issue Tracker":"https://github.com/ousret/charset_normalizer/issues"}}}`)}},
				{URL: "https://pypi.org/pypi/charset-normalizer/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","project_urls":{"Source":"https://github.com/jawah/charset_normalizer"}}}`)}},
			},
			want: "https://github.com/jawah/charset_normalizer",
		},
		{
			name: "a release without links falls back to the project",
			calls: []httpxtest.Call{
				{URL: "https://pypi.org/pypi/charset-normalizer/2.0.12/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","version":"2.0.12"}}`)}},
				{URL: "https://pypi.org/pypi/charset-normalizer/json", Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(`{"info":{"name":"charset-normalizer","project_urls":{"Code":"https://github.com/jawah/charset_normalizer"}}}`)}},
			},
			want: "https://github.com/jawah/charset_normalizer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := rebuild.RegistryMux{PyPI: pypireg.HTTPRegistry{Client: &httpxtest.MockClient{Calls: tc.calls, URLValidator: httpxtest.NewURLValidator(t)}}}
			got, err := Rebuilder{}.InferRepo(context.Background(), target, mux)
			if err != nil {
				t.Fatalf("InferRepo() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("InferRepo() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPythonTag(t *testing.T) {
	setuptools := []string{"wheel==0.36.2", "setuptools<=56.2.0"}
	for _, tc := range []struct {
		filename string
		reqs     []string
		want     string
	}{
		{"asn1crypto-1.5.1-py2.py3-none-any.whl", setuptools, "py2.py3"},
		{"google_pasta-0.2.0-py2-none-any.whl", setuptools, "py2"},
		{"dacite-1.9.2-py3-none-any.whl", setuptools, ""},
		{"six-1.16.0-py2.py3-none-any.whl", []string{"flit_core==3.9.0"}, ""},
		{"pkg-1.0.tar.gz", setuptools, ""},
	} {
		if got := pythonTag(tc.filename, tc.reqs); got != tc.want {
			t.Errorf("pythonTag(%q, %v) = %q, want %q", tc.filename, tc.reqs, got, tc.want)
		}
	}
}

func TestInferStrategyLocatedError(t *testing.T) {
	const releaseURL = "https://pypi.org/pypi/test-package/1.0.0/json"
	const wheelURL = "https://files.pythonhosted.org/test_package-1.0.0-py3-none-any.whl"
	release := func(urls string) string {
		return `{"info":{"name":"test-package","version":"1.0.0"},"urls":[` + urls + `]}`
	}
	wheel := `{"filename":"test_package-1.0.0-py3-none-any.whl","url":"` + wheelURL + `","size":9,"upload_time_iso_8601":"2023-01-01T12:00:00.000000Z"}`
	for _, tc := range []struct {
		name        string
		calls       []httpxtest.Call
		wantLocated bool // the error carries the resolved location
	}{
		{
			name: "artifact missing from the release",
			calls: []httpxtest.Call{
				{URL: releaseURL, Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(release(""))}},
			},
		},
		{
			name: "unreadable wheel",
			calls: []httpxtest.Call{
				{URL: releaseURL, Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(release(wheel))}},
				{URL: releaseURL, Response: &http.Response{StatusCode: 200, Body: httpxtest.Body(release(wheel))}},
				{URL: wheelURL, Response: &http.Response{StatusCode: 200, Body: httpxtest.Body("not a zip")}},
			},
			wantLocated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(`commits:
  - id: initial-commit
    files:
      README.md: |
        # test-package
`, nil))
			ref := repo.Commits["initial-commit"].String()
			target := rebuild.Target{Ecosystem: rebuild.PyPI, Package: "test-package", Version: "1.0.0", Artifact: "test_package-1.0.0-py3-none-any.whl"}
			mux := rebuild.RegistryMux{PyPI: pypireg.HTTPRegistry{Client: &httpxtest.MockClient{Calls: tc.calls, URLValidator: httpxtest.NewURLValidator(t)}}}
			rcfg := &rebuild.RepoConfig{Repo: gitx.Repo{Repository: repo.Repository}, URI: "https://github.com/test-org/test-package"}
			s, err := Rebuilder{}.InferStrategy(context.Background(), target, mux, rcfg, &rebuild.LocationHint{Location: rebuild.Location{Repo: rcfg.URI, Ref: ref}})
			if err == nil {
				t.Fatalf("InferStrategy expected error, got %v", s)
			}
			var located *rebuild.InferenceError
			if errors.As(err, &located) != tc.wantLocated {
				t.Fatalf("located error = %v, want %v: %v", !tc.wantLocated, tc.wantLocated, err)
			}
			if !tc.wantLocated {
				return
			}
			want := rebuild.InferenceErrorDetail{Location: rebuild.Location{Repo: rcfg.URI, Ref: ref}, Published: time.Date(2023, time.January, 1, 12, 0, 0, 0, time.UTC)}
			if diff := cmp.Diff(want, located.Detail); diff != "" {
				t.Errorf("located detail diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractPlatformTag(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			name:     "standard 5-part wheel",
			filename: "foo-1.0.0-cp310-cp310-manylinux_2_17_x86_64.whl",
			want:     "manylinux_2_17_x86_64",
		},
		{
			name:     "6-part wheel with build tag",
			filename: "foo-1.0.0-1-cp310-cp310-manylinux_2_17_x86_64.whl",
			want:     "manylinux_2_17_x86_64",
		},
		{
			name:     "pure wheel",
			filename: "foo-1.0.0-py3-none-any.whl",
			want:     "any",
		},
		{
			name:     "compressed platform tags",
			filename: "foo-2.4.3-cp314-cp314-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl",
			want:     "manylinux_2_27_x86_64.manylinux_2_28_x86_64",
		},
		{
			name:     "non-wheel",
			filename: "foo-1.0.0.tar.gz",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPlatformTag(tt.filename); got != tt.want {
				t.Errorf("extractPlatformTag(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestFindPlatformWheel(t *testing.T) {
	artifacts := []pypireg.Artifact{
		{Filename: "foo-1.0.0.tar.gz"},
		{Filename: "foo-1.0.0-py3-none-any.whl"},
		{Filename: "foo-1.0.0-cp310-cp310-win_amd64.whl"},
		{Filename: "foo-1.0.0-cp310-cp310-macosx_11_0_arm64.whl"},
		{Filename: "foo-1.0.0-cp310-cp310-manylinux2014_x86_64.whl"},
	}
	art, err := FindPlatformWheel(artifacts)
	if err != nil {
		t.Fatalf("FindPlatformWheel() error = %v", err)
	}
	if art.Filename != "foo-1.0.0-cp310-cp310-manylinux2014_x86_64.whl" {
		t.Errorf("FindPlatformWheel() = %v, want foo-1.0.0-cp310-cp310-manylinux2014_x86_64.whl", art.Filename)
	}
}

func TestExtractWheelTags(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     WheelTags
	}{
		{
			name:     "standard 5-part wheel",
			filename: "foo-1.0.0-cp310-cp310-manylinux_2_17_x86_64.whl",
			want: WheelTags{
				Python:   "cp310",
				ABI:      "cp310",
				Platform: "manylinux_2_17_x86_64",
			},
		},
		{
			name:     "abi3 wheel",
			filename: "cryptography-41.0.0-cp37-abi3-manylinux_2_28_x86_64.whl",
			want: WheelTags{
				Python:   "cp37",
				ABI:      "abi3",
				Platform: "manylinux_2_28_x86_64",
			},
		},
		{
			name:     "6-part wheel with build tag",
			filename: "foo-1.0.0-1-cp310-cp310-manylinux_2_17_x86_64.whl",
			want: WheelTags{
				Python:   "cp310",
				ABI:      "cp310",
				Platform: "manylinux_2_17_x86_64",
			},
		},
		{
			name:     "compressed platform tag set",
			filename: "foo-2.4.3-cp310-cp310-manylinux1_x86_64.manylinux_2_28_x86_64.whl",
			want: WheelTags{
				Python:   "cp310",
				ABI:      "cp310",
				Platform: "manylinux1_x86_64.manylinux_2_28_x86_64",
			},
		},
		{
			name:     "non-wheel",
			filename: "foo-1.0.0.tar.gz",
			want:     WheelTags{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWheelTags(tt.filename)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("extractWheelTags(%q) diff (-want +got):\n%s", tt.filename, diff)
			}
		})
	}
}

func TestBackendRequirements(t *testing.T) {
	for _, tc := range []struct {
		name string
		reqs []string
		want []string
	}{
		{
			name: "MesonPythonAddsNinja",
			reqs: []string{"meson-python>=0.15.0", "Cython>=3.0.4"},
			want: []string{"ninja"},
		},
		{
			name: "ScikitBuildCoreAddsNinja",
			reqs: []string{"scikit-build-core==0.10.7"},
			want: []string{"ninja"},
		},
		{
			name: "SetuptoolsDoesNotAddNinja",
			reqs: []string{"setuptools>=61", "wheel"},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := implicitRequirements(tc.reqs)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("backendRequirements(%v) diff (-want +got):\n%s", tc.reqs, diff)
			}
		})
	}
}

// testPins is a cibuildwheel pin table with the shapes that inference has to
// handle: a pin whose tag expired before its digest was recorded, and a
// maintenance release of an older major version published after a newer one.
var testPins = platform.CibuildwheelPinTable{
	{
		Version:   "2.0.0",
		Published: time.Date(2021, time.July, 1, 0, 0, 0, 0, time.UTC),
		Commit:    testCommit("4"),
		Images: map[string]platform.PinnedImage{
			platform.ImageManylinux2014X86_64: {Tag: "2021-06-01-expired"},
		},
	},
	{
		Version:   "2.16.2",
		Published: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
		Commit:    testCommit("5"),
		Images: map[string]platform.PinnedImage{
			platform.ImageManylinux2014X86_64: {Tag: "2023-10-03-72cdc42", Digest: testDigest("a")},
			platform.ImageManylinux2_28X86_64: {Tag: "2023-10-03-72cdc42", Digest: testDigest("b")},
			platform.ImageMusllinux1_1X86_64:  {Tag: "2023-10-03-72cdc42", Digest: testDigest("c")},
		},
	},
	{
		Version:   "3.4.0",
		Published: time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC),
		Commit:    testCommit("6"),
		Images: map[string]platform.PinnedImage{
			platform.ImageManylinux2014X86_64: {Tag: "2026.03.01-1", Digest: testDigest("d")},
			platform.ImageManylinux2_28X86_64: {Tag: "2026.03.01-1", Digest: testDigest("e")},
			platform.ImageMusllinux1_2X86_64:  {Tag: "2026.03.01-1", Digest: testDigest("f")},
		},
	},
	{
		Version:   "2.23.4",
		Published: time.Date(2026, time.March, 15, 0, 0, 0, 0, time.UTC),
		Commit:    testCommit("7"),
		Images: map[string]platform.PinnedImage{
			platform.ImageManylinux2014X86_64: {Tag: "2026.02.20-1", Digest: testDigest("1")},
			platform.ImageManylinux2_28X86_64: {Tag: "2026.02.20-1", Digest: testDigest("2")},
			platform.ImageMusllinux1_2X86_64:  {Tag: "2026.02.20-1", Digest: testDigest("3")},
		},
	},
}

// testDigest returns a well-formed digest made of the character c.
func testDigest(c string) string {
	return "sha256:" + strings.Repeat(c, 64)
}

// testCommit returns a well-formed commit hash made of the character c.
func testCommit(c string) string {
	return strings.Repeat(c, 40)
}

func TestInferBaseImage(t *testing.T) {
	tests := []struct {
		name        string
		platformTag string
		cibwSpecs   []string
		uploaded    time.Time
		want        string
	}{
		{
			name:        "Musllinux",
			platformTag: "musllinux_1_2_x86_64",
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/musllinux_1_2_x86_64:2026.03.01-1@" + testDigest("f"),
		},
		{
			// 2.23.4 was published after 3.4.0 but an unpinned install
			// resolves to the highest version, which pins the newer images.
			name:        "CompressedTagSetAfterMaintenanceRelease",
			platformTag: "manylinux_2_27_x86_64.manylinux_2_28_x86_64",
			uploaded:    time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/manylinux_2_28_x86_64:2026.03.01-1@" + testDigest("e"),
		},
		{
			name:        "DroppedRepository",
			platformTag: "musllinux_1_1_x86_64",
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/musllinux_1_1_x86_64:2023-10-03-72cdc42@" + testDigest("c"),
		},
		{
			name:        "ConfigVersion",
			platformTag: "manylinux2014_x86_64",
			cibwSpecs:   []string{"2.16.2"},
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/manylinux2014_x86_64:2023-10-03-72cdc42@" + testDigest("a"),
		},
		{
			// A version newer than the pin table falls back to the upload time.
			name:        "UnknownConfigVersion",
			platformTag: "musllinux_1_2_x86_64",
			cibwSpecs:   []string{"9.9.9"},
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/musllinux_1_2_x86_64:2026.03.01-1@" + testDigest("f"),
		},
		{
			// A version that does not pin the repository falls back to the
			// upload time, which selects the highest version that does.
			name:        "ConfigVersionWithoutRepository",
			platformTag: "musllinux_1_1_x86_64",
			cibwSpecs:   []string{"3.4.0"},
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "quay.io/pypa/musllinux_1_1_x86_64:2023-10-03-72cdc42@" + testDigest("c"),
		},
		{
			name:        "ExpiredPin",
			platformTag: "manylinux2014_x86_64",
			uploaded:    time.Date(2021, time.August, 1, 0, 0, 0, 0, time.UTC),
			want:        "",
		},
		{
			name:        "BeforeFirstPin",
			platformTag: "manylinux2014_x86_64",
			uploaded:    time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:        "",
		},
		{
			name:        "UnsupportedArchitecture",
			platformTag: "manylinux2014_aarch64",
			uploaded:    time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferBaseImage(testPins, tt.platformTag, tt.uploaded, tt.cibwSpecs...); got != tt.want {
				t.Errorf("inferBaseImage(%q, %v, %v) = %q, want %q", tt.platformTag, tt.uploaded, tt.cibwSpecs, got, tt.want)
			}
		})
	}
}

func TestInferBuildRoot(t *testing.T) {
	repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "initial-commit", Files: gitxtest.FileContent{
		"src/module.c":        "int answer = 42;\n",
		"include/lib/log.h":   "",
		"include/lib/array.h": "",
	}}}, nil))
	tree := must(must(repo.CommitObject(repo.Commits["initial-commit"])).Tree())
	tests := []struct {
		name   string
		module []byte
		want   string
	}{
		{
			name:   "DebugInfo",
			module: elfWithUnits(compileUnit{Name: "src/module.c", CompDir: "/tmp/build/pkg"}),
			want:   "/tmp/build/pkg",
		},
		{
			name:   "StrippedWithRodata",
			module: elfWithRodata("/tmp/build/pkg/src/module.c"),
			want:   "/tmp/build/pkg",
		},
		{
			// Headers that a CI step installed into a system directory must
			// not outvote the sources built in the project.
			name:   "StrippedWithInstalledHeaders",
			module: elfWithRodata("/project/src/module.c", "/usr/local/include/lib/log.h", "/usr/local/include/lib/array.h"),
			want:   "/project",
		},
		{
			name:   "Stripped",
			module: elfWithUnits(),
			want:   "",
		},
		{
			name:   "UnmatchedDebugInfo",
			module: elfWithUnits(compileUnit{Name: "build/__native.c", CompDir: "/project"}),
			want:   "",
		},
		{
			name:   "UnusableRoot",
			module: elfWithUnits(compileUnit{Name: "src/module.c", CompDir: "/home/runner/my project"}),
			want:   "",
		},
		{
			name:   "CheckoutRoot",
			module: elfWithUnits(compileUnit{Name: "src/module.c", CompDir: "/src"}),
			want:   "",
		},
		{
			name:   "MalformedModule",
			module: []byte("\x7fELF\x02\x01"),
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zr := readWheel(t, zipEntry("pkg/module.cpython-312-x86_64-linux-gnu.so", string(tt.module)))
			if got := inferBuildRoot(zr, tree); got != tt.want {
				t.Errorf("inferBuildRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}
