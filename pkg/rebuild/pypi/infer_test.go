// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
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
		{"flit_core==3.7.1", "flit_core"},
		{"setuptools-scm<6.0", "setuptools-scm"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := requirementName(tt.req); got != tt.want {
			t.Errorf("requirementName(%q) = %q, want %q", tt.req, got, tt.want)
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
