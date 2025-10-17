// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TestDiff(t *testing.T) {
	testCases := []struct {
		name           string
		description    string
		left           func() ([]byte, error)
		right          func() ([]byte, error)
		leftName       string
		rightName      string
		expectMatch    bool
		expectTextDiff string
		expectJSONDiff string
	}{
		// Text file comparisons
		{
			name:        "identical_text_files",
			description: "Two identical text files should match",
			left: func() ([]byte, error) {
				return []byte("hello world\nthis is a test\n"), nil
			},
			right: func() ([]byte, error) {
				return []byte("hello world\nthis is a test\n"), nil
			},
			leftName:    "file.txt",
			rightName:   "file.txt",
			expectMatch: true,
			expectTextDiff: `--- file.txt
+++ file.txt
`,
			expectJSONDiff: `{
  "source1": "file.txt",
  "source2": "file.txt"
}`,
		},
		{
			name:        "different_text_files",
			description: "Different text files should not match and show diff",
			left: func() ([]byte, error) {
				return []byte("hello world\n"), nil
			},
			right: func() ([]byte, error) {
				return []byte("hello there\n"), nil
			},
			leftName:    "file.txt",
			rightName:   "file.txt",
			expectMatch: false,
			expectTextDiff: `--- file.txt
+++ file.txt
@@ -1 +1 @@
-hello world
+hello there
`,
			expectJSONDiff: `{
  "source1": "file.txt",
  "source2": "file.txt",
  "unified_diff": "@@ -1 +1 @@\n-hello world\n+hello there\n"
}`,
		},
		{
			name:        "text_vs_binary",
			description: "Text vs binary should note type mismatch",
			left: func() ([]byte, error) {
				return []byte("hello world"), nil
			},
			right: func() ([]byte, error) {
				return []byte{0x00, 0x01, 0x02, 0x03, 0x00}, nil
			},
			leftName:    "file.txt",
			rightName:   "file.bin",
			expectMatch: false,
			expectTextDiff: `--- file.txt
+++ file.bin
│┄ File types differ: text vs binary
`,
			expectJSONDiff: `{
  "source1": "file.txt",
  "source2": "file.bin",
  "comments": ["File types differ: text vs binary"]
}`,
		},
		// Binary file comparisons
		{
			name:        "identical_binary_files",
			description: "Two identical binary files should match",
			left: func() ([]byte, error) {
				return bytes.Repeat([]byte{0x00, 0xFF, 0xAA, 0x55}, 10), nil
			},
			right: func() ([]byte, error) {
				return bytes.Repeat([]byte{0x00, 0xFF, 0xAA, 0x55}, 10), nil
			},
			leftName:    "file.bin",
			rightName:   "file.bin",
			expectMatch: true,
			expectTextDiff: `--- file.bin
+++ file.bin
`,
			expectJSONDiff: `{
  "source1": "file.bin",
  "source2": "file.bin"
}`,
		},
		{
			name:        "different_binary_files",
			description: "Different binary files should note they differ",
			left: func() ([]byte, error) {
				return []byte{0x00, 0x01, 0x02, 0x03}, nil
			},
			right: func() ([]byte, error) {
				return []byte{0x00, 0x01, 0xFF, 0x03}, nil
			},
			leftName:    "file.bin",
			rightName:   "file.bin",
			expectMatch: false,
			expectTextDiff: `--- file.bin
+++ file.bin
│┄ Binary files differ
`,
			expectJSONDiff: `{
  "source1": "file.bin",
  "source2": "file.bin",
  "comments": ["Binary files differ"]
}`,
		},
		// TAR archive comparisons
		{
			name:        "identical_tar_archives",
			description: "Two identical tar archives should match",
			left: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
					{Header: &tar.Header{Name: "file2.txt", Size: 5, Mode: 0644}, Body: []byte("world")},
				})
			},
			right: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
					{Header: &tar.Header{Name: "file2.txt", Size: 5, Mode: 0644}, Body: []byte("world")},
				})
			},
			leftName:    "archive.tar",
			rightName:   "archive.tar",
			expectMatch: true,
			expectTextDiff: `--- archive.tar
+++ archive.tar
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar"
}`,
		},
		{
			name:        "tar_missing_file",
			description: "TAR with missing file should show in file list diff",
			left: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
				})
			},
			right: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
					{Header: &tar.Header{Name: "file2.txt", Size: 5, Mode: 0644}, Body: []byte("world")},
				})
			},
			leftName:    "archive.tar",
			rightName:   "archive.tar",
			expectMatch: false,
			expectTextDiff: `--- archive.tar
+++ archive.tar
├── file list
│ @@ -1 +1,2 @@
│  -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file1.txt
│ +-rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file2.txt
├── archive.tar/file2.txt
│┄ Entry only in second archive
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar",
  "details": [
    {
      "source1": "file list",
      "source2": "file list",
      "unified_diff": "@@ -1 +1,2 @@\n -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file1.txt\n+-rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file2.txt\n"
    },
    {
      "source1": "archive.tar/file2.txt",
      "source2": "archive.tar/file2.txt",
      "comments": ["Entry only in second archive"]
    }
  ]
}`,
		},
		{
			name:        "tar_content_diff",
			description: "TAR with different file content should show diff",
			left: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "data.txt", Size: 11, Mode: 0644}, Body: []byte("hello world")},
				})
			},
			right: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "data.txt", Size: 11, Mode: 0644}, Body: []byte("hello there")},
				})
			},
			leftName:    "archive.tar",
			rightName:   "archive.tar",
			expectMatch: false,
			expectTextDiff: `--- archive.tar
+++ archive.tar
├── archive.tar/data.txt
│ @@ -1 +1 @@
│ -hello world
│ +hello there
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar",
  "details": [
    {
      "source1": "archive.tar/data.txt",
      "source2": "archive.tar/data.txt",
      "unified_diff": "@@ -1 +1 @@\n-hello world\n+hello there\n"
    }
  ]
}`,
		},
		// GZIP comparisons (simplified for now)
		{
			name:        "different_gzip_text",
			description: "Different gzipped text files should show inner diff",
			left: func() ([]byte, error) {
				return createGzip([]byte("version 1.0\n"))
			},
			right: func() ([]byte, error) {
				return createGzip([]byte("version 2.0\n"))
			},
			leftName:    "file.txt.gz",
			rightName:   "file.txt.gz",
			expectMatch: false,
			expectTextDiff: `--- file.txt.gz
+++ file.txt.gz
├── file.txt
│ @@ -1 +1 @@
│ -version 1.0
│ +version 2.0
`,
			expectJSONDiff: `{
  "source1": "file.txt.gz",
  "source2": "file.txt.gz",
  "details": [
    {
      "source1": "file.txt",
      "source2": "file.txt",
      "unified_diff": "@@ -1 +1 @@\n-version 1.0\n+version 2.0\n"
    }
  ]
}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate test data
			leftData, err := tc.left()
			if err != nil {
				t.Fatalf("Failed to create left data: %v", err)
			}
			rightData, err := tc.right()
			if err != nil {
				t.Fatalf("Failed to create right data: %v", err)
			}

			// Create File structs
			leftFile := File{
				Name:   tc.leftName,
				Reader: bytes.NewReader(leftData),
			}
			rightFile := File{
				Name:   tc.rightName,
				Reader: bytes.NewReader(rightData),
			}

			// Test text output
			t.Run("text_output", func(t *testing.T) {
				opts := Options{OutputJSON: false}
				match, output, err := Diff(leftFile, rightFile, opts)
				if err != nil {
					t.Fatalf("Diff failed: %v", err)
				}

				// Check match result
				if match != tc.expectMatch {
					t.Errorf("Expected match=%v, got %v", tc.expectMatch, match)
				}

				// Compare exact output
				if tc.expectTextDiff != "" && output != tc.expectTextDiff {
					t.Errorf("Text output mismatch\nExpected:\n%q\nGot:\n%q", tc.expectTextDiff, output)
					// Also show a diff of the outputs for easier debugging
					t.Logf("Character-by-character comparison:")
					minLen := min(len(output), len(tc.expectTextDiff))
					for i := range minLen {
						if tc.expectTextDiff[i] != output[i] {
							t.Logf("First difference at position %d: expected %q got %q",
								i, tc.expectTextDiff[i], output[i])
							break
						}
					}
					if len(tc.expectTextDiff) != len(output) {
						t.Logf("Length mismatch: expected %d, got %d",
							len(tc.expectTextDiff), len(output))
					}
				}
			})

			// Test JSON output
			t.Run("json_output", func(t *testing.T) {
				opts := Options{OutputJSON: true}
				match, output, err := Diff(leftFile, rightFile, opts)
				if err != nil {
					t.Fatalf("Diff failed: %v", err)
				}

				// Check match result
				if match != tc.expectMatch {
					t.Errorf("Expected match=%v, got %v", tc.expectMatch, match)
				}

				// Parse both expected and actual JSON for comparison
				if tc.expectJSONDiff != "" {
					var expectedNode, actualNode any
					if err := json.Unmarshal([]byte(tc.expectJSONDiff), &expectedNode); err != nil {
						t.Fatalf("Failed to parse expected JSON: %v\nJSON: %s", err, tc.expectJSONDiff)
					}
					if err := json.Unmarshal([]byte(output), &actualNode); err != nil {
						t.Fatalf("Failed to parse actual JSON: %v\nJSON: %s", err, output)
					}

					// Compare the parsed JSON structures
					expectedBytes, _ := json.MarshalIndent(expectedNode, "", "  ")
					actualBytes, _ := json.MarshalIndent(actualNode, "", "  ")

					if string(expectedBytes) != string(actualBytes) {
						t.Errorf("JSON output mismatch\nExpected:\n%s\nGot:\n%s",
							string(expectedBytes), string(actualBytes))
					}
				}
			})
		})
	}
}

// Test helper functions

func createTar(entries []archive.TarEntry) ([]byte, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	for _, entry := range entries {
		if err := tw.WriteHeader(entry.Header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(entry.Body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func createGzip(data []byte) ([]byte, error) {
	buf := new(bytes.Buffer)
	gw := gzip.NewWriter(buf)
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
