// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

func TestInferPythonVersion(t *testing.T) {
	tests := []struct {
		name string
		reqs []string
		want string
	}{
		{
			name: "no setuptools",
			reqs: []string{"wheel", "requests"},
			want: "",
		},
		{
			name: "setuptools exactly 60",
			reqs: []string{"setuptools==60.0.0"},
			want: "",
		},
		{
			name: "setuptools greater than 60",
			reqs: []string{"setuptools>=60.0.0"},
			want: "",
		},
		{
			name: "setuptools less than 60",
			reqs: []string{"setuptools<60"},
			want: "3.11",
		},
		{
			name: "setuptools less than or equal 59",
			reqs: []string{"setuptools<=59"},
			want: "3.11",
		},
		{
			name: "setuptools exactly 58",
			reqs: []string{"setuptools==58.1.0"},
			want: "3.11",
		},
		{
			name: "complex constraint matching",
			reqs: []string{"setuptools>=40.0,<60.0"},
			want: "3.11",
		},
		{
			name: "case insensitive",
			reqs: []string{"SetupTools<60"},
			want: "3.11",
		},
		{
			name: "with extras",
			reqs: []string{"setuptools[core]<60"},
			want: "3.11",
		},
		{
			name: "setuptools-scm should not match",
			reqs: []string{"setuptools-scm<60"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferPythonVersion(tt.reqs); got != tt.want {
				t.Errorf("inferPythonVersion() = %v, want %v", got, tt.want)
			}
		})
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
