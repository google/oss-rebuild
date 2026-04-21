// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"testing"
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

func TestInferRequirementsAcceptsEquivalentDistInfoNames(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		version string
		files   map[string]string
		want    []string
	}{
		{
			name:    "exact normalized path",
			pkg:     "friendly-bard",
			version: "1.2.3",
			files: map[string]string{
				"friendly_bard-1.2.3.dist-info/WHEEL":    "Wheel-Version: 1.0\nGenerator: bdist_wheel (0.43.0)\nTag: py3-none-any\n",
				"friendly_bard-1.2.3.dist-info/METADATA": "Metadata-Version: 2.1\nName: friendly-bard\n",
			},
			want: []string{"wheel==0.43.0", "setuptools==56.2.0"},
		},
		{
			name:    "lowercased historical path",
			pkg:     "128Autograder",
			version: "5.2.3",
			files: map[string]string{
				"128autograder-5.2.3.dist-info/WHEEL":    "Wheel-Version: 1.0\nGenerator: bdist_wheel (0.43.0)\nTag: py3-none-any\n",
				"128autograder-5.2.3.dist-info/METADATA": "Metadata-Version: 2.1\nName: 128Autograder\n",
			},
			want: []string{"wheel==0.43.0", "setuptools==56.2.0"},
		},
		{
			name:    "dot and hyphen equivalence",
			pkg:     "Friendly.Bard",
			version: "2.0.0",
			files: map[string]string{
				"friendly_bard-2.0.0.dist-info/WHEEL":    "Wheel-Version: 1.0\nGenerator: bdist_wheel (0.43.0)\nTag: py3-none-any\n",
				"friendly_bard-2.0.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: Friendly.Bard\n",
			},
			want: []string{"wheel==0.43.0", "setuptools==56.2.0"},
		},
		{
			name:    "historical uppercase hyphenated path",
			pkg:     "friendly-bard",
			version: "3.1.4",
			files: map[string]string{
				"Friendly-Bard-3.1.4.dist-info/WHEEL":    "Wheel-Version: 1.0\nGenerator: bdist_wheel (0.43.0)\nTag: py3-none-any\n",
				"Friendly-Bard-3.1.4.dist-info/METADATA": "Metadata-Version: 2.1\nName: friendly-bard\n",
			},
			want: []string{"wheel==0.43.0", "setuptools==56.2.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zr := testZipReader(t, tt.files)
			got, err := inferRequirements(tt.pkg, tt.version, zr)
			if err != nil {
				t.Fatalf("inferRequirements() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("inferRequirements() len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("inferRequirements()[%d] = %q, want %q; got %v", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func testZipReader(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader(): %v", err)
	}
	return zr
}
