// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
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
		// Simple file comparisons
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
			leftName:  "file.txt",
			rightName: "file.txt",
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
			leftName:  "file.txt",
			rightName: "file.bin",
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
			leftName:  "file.bin",
			rightName: "file.bin",
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
			leftName:  "archive.tar",
			rightName: "archive.tar",
			expectTextDiff: `--- archive.tar
+++ archive.tar
├── file list
│ @@ -1 +1,2 @@
│  -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file1.txt
│ +-rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file2.txt
├── file2.txt
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
      "source1": "file2.txt",
      "source2": "file2.txt",
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
			leftName:  "archive.tar",
			rightName: "archive.tar",
			expectTextDiff: `--- archive.tar
+++ archive.tar
├── data.txt
│ @@ -1 +1 @@
│ -hello world
│ +hello there
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar",
  "details": [
    {
      "source1": "data.txt",
      "source2": "data.txt",
      "unified_diff": "@@ -1 +1 @@\n-hello world\n+hello there\n"
    }
  ]
}`,
		},
		{
			name:        "tar_with_file_added_in_middle",
			description: "TAR with file added in middle should preserve original order",
			left: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "a_file.txt", Size: 5, Mode: 0644}, Body: []byte("aaaaa")},
					{Header: &tar.Header{Name: "c_file.txt", Size: 5, Mode: 0644}, Body: []byte("ccccc")},
				})
			},
			right: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "a_file.txt", Size: 5, Mode: 0644}, Body: []byte("aaaaa")},
					{Header: &tar.Header{Name: "b_file.txt", Size: 5, Mode: 0644}, Body: []byte("bbbbb")},
					{Header: &tar.Header{Name: "c_file.txt", Size: 5, Mode: 0644}, Body: []byte("ccccc")},
				})
			},
			leftName:  "archive.tar",
			rightName: "archive.tar",
			expectTextDiff: `--- archive.tar
+++ archive.tar
├── file list
│ @@ -1,2 +1,3 @@
│  -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 a_file.txt
│ +-rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 b_file.txt
│  -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 c_file.txt
├── b_file.txt
│┄ Entry only in second archive
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar",
  "details": [
    {
      "source1": "file list",
      "source2": "file list",
      "unified_diff": "@@ -1,2 +1,3 @@\n -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 a_file.txt\n+-rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 b_file.txt\n -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 c_file.txt\n"
    },
    {
      "source1": "b_file.txt",
      "source2": "b_file.txt",
      "comments": ["Entry only in second archive"]
    }
  ]
}`,
		},
		{
			name:        "tar_with_files_reordered",
			description: "TAR with same files in different order should show reordering comment",
			left: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "z_last.txt", Size: 5, Mode: 0644}, Body: []byte("zzzzz")},
					{Header: &tar.Header{Name: "a_first.txt", Size: 5, Mode: 0644}, Body: []byte("aaaaa")},
					{Header: &tar.Header{Name: "m_middle.txt", Size: 5, Mode: 0644}, Body: []byte("mmmmm")},
				})
			},
			right: func() ([]byte, error) {
				return createTar([]archive.TarEntry{
					{Header: &tar.Header{Name: "a_first.txt", Size: 5, Mode: 0644}, Body: []byte("aaaaa")},
					{Header: &tar.Header{Name: "m_middle.txt", Size: 5, Mode: 0644}, Body: []byte("mmmmm")},
					{Header: &tar.Header{Name: "z_last.txt", Size: 5, Mode: 0644}, Body: []byte("zzzzz")},
				})
			},
			leftName:  "archive.tar",
			rightName: "archive.tar",
			expectTextDiff: `--- archive.tar
+++ archive.tar
│┄ Entry order differs (listings shown in sorted order)
`,
			expectJSONDiff: `{
  "source1": "archive.tar",
  "source2": "archive.tar",
  "comments": ["Entry order differs (listings shown in sorted order)"]
}`,
		},
		// GZIP comparisons
		{
			name:        "different_gzip_text",
			description: "Different gzipped text files should show inner diff",
			left: func() ([]byte, error) {
				return createGzip([]byte("version 1.0\n"))
			},
			right: func() ([]byte, error) {
				return createGzip([]byte("version 2.0\n"))
			},
			leftName:  "file.txt.gz",
			rightName: "file.txt.gz",

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
		// TAR GZIP archive comparisons
		{
			name:        "identical_targz_archives",
			description: "Two identical tar.gz archives should match",
			left: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
					{Header: &tar.Header{Name: "file2.txt", Size: 5, Mode: 0644}, Body: []byte("world")},
				})
			},
			right: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "file1.txt", Size: 5, Mode: 0644}, Body: []byte("hello")},
					{Header: &tar.Header{Name: "file2.txt", Size: 5, Mode: 0644}, Body: []byte("world")},
				})
			},
			leftName:       "archive.tar.gz",
			rightName:      "archive.tar.gz",
			expectMatch:    true,
			expectTextDiff: "",
			expectJSONDiff: "",
		},
		{
			name:        "targz_content_diff",
			description: "TAR GZIP with different file content should show nested diff",
			left: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "config.txt", Size: 8, Mode: 0644}, Body: []byte("debug=on")},
				})
			},
			right: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "config.txt", Size: 9, Mode: 0644}, Body: []byte("debug=off")},
				})
			},
			leftName:  "archive.tar.gz",
			rightName: "archive.tar.gz",
			expectTextDiff: `--- archive.tar.gz
+++ archive.tar.gz
│   --- archive.tar
├─┐ +++ archive.tar
│ ├── file list
│ │ @@ -1 +1 @@
│ │ --rw-r--r-- 0 0            8 1970-01-01 00:00:00.000000 config.txt
│ │ +-rw-r--r-- 0 0            9 1970-01-01 00:00:00.000000 config.txt
│ ├── config.txt
│ │ @@ -1 +1 @@
│ │ -debug=on
│ │ +debug=off
`,
			expectJSONDiff: `{
  "source1": "archive.tar.gz",
  "source2": "archive.tar.gz",
  "details": [
    {
      "source1": "archive.tar",
      "source2": "archive.tar",
      "details": [
        {
          "source1": "file list",
          "source2": "file list",
          "unified_diff": "@@ -1 +1 @@\n--rw-r--r-- 0 0            8 1970-01-01 00:00:00.000000 config.txt\n+-rw-r--r-- 0 0            9 1970-01-01 00:00:00.000000 config.txt\n"
        },
        {
          "source1": "config.txt",
          "source2": "config.txt",
          "unified_diff": "@@ -1 +1 @@\n-debug=on\n+debug=off\n"
        }
      ]
    }
  ]
}`,
		},
		{
			name:        "tgz_extension_content_diff",
			description: "TGZ (alternate extension) should work the same as tar.gz",
			left: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "data.json", Size: 9, Mode: 0644}, Body: []byte(`{"v":"1"}`)},
				})
			},
			right: func() ([]byte, error) {
				return createTgz([]archive.TarEntry{
					{Header: &tar.Header{Name: "data.json", Size: 9, Mode: 0644}, Body: []byte(`{"v":"2"}`)},
				})
			},
			leftName:  "archive.tgz",
			rightName: "archive.tgz",
			expectTextDiff: `--- archive.tgz
+++ archive.tgz
│   --- archive.tar
├─┐ +++ archive.tar
│ ├── data.json
│ │ @@ -1 +1 @@
│ │ -{"v":"1"}
│ │ +{"v":"2"}
`,
			expectJSONDiff: `{
  "source1": "archive.tgz",
  "source2": "archive.tgz",
  "details": [
    {
      "source1": "archive.tar",
      "source2": "archive.tar",
      "details": [
        {
          "source1": "data.json",
          "source2": "data.json",
          "unified_diff": "@@ -1 +1 @@\n-{\"v\":\"1\"}\n+{\"v\":\"2\"}\n"
        }
      ]
    }
  ]
}`,
		},
		// ZIP archive comparisons
		{
			name:        "identical_zip_archives",
			description: "Two identical zip archives should match",
			left: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "file1.txt"}, Body: []byte("hello")},
					{FileHeader: &zip.FileHeader{Name: "file2.txt"}, Body: []byte("world")},
				})
			},
			right: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "file1.txt"}, Body: []byte("hello")},
					{FileHeader: &zip.FileHeader{Name: "file2.txt"}, Body: []byte("world")},
				})
			},
			leftName:       "archive.zip",
			rightName:      "archive.zip",
			expectMatch:    true,
			expectTextDiff: "",
			expectJSONDiff: "",
		},
		{
			name:        "zip_missing_file",
			description: "ZIP with missing file should show in file list diff",
			left: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "file1.txt"}, Body: []byte("hello")},
				})
			},
			right: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "file1.txt"}, Body: []byte("hello")},
					{FileHeader: &zip.FileHeader{Name: "file2.txt"}, Body: []byte("world")},
				})
			},
			leftName:  "archive.zip",
			rightName: "archive.zip",
			expectTextDiff: `--- archive.zip
+++ archive.zip
├── file list
│ @@ -1 +1,2 @@
│  -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 file1.txt
│ +-rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 file2.txt
├── file2.txt
│┄ Entry only in second archive
`,
			expectJSONDiff: `{
  "source1": "archive.zip",
  "source2": "archive.zip",
  "details": [
    {
      "source1": "file list",
      "source2": "file list",
      "unified_diff": "@@ -1 +1,2 @@\n -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 file1.txt\n+-rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 file2.txt\n"
    },
    {
      "source1": "file2.txt",
      "source2": "file2.txt",
      "comments": ["Entry only in second archive"]
    }
  ]
}`,
		},
		{
			name:        "zip_content_diff",
			description: "ZIP with different file content should show diff",
			left: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "readme.txt"}, Body: []byte("version 1.0\n")},
				})
			},
			right: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "readme.txt"}, Body: []byte("version 2.0\n")},
				})
			},
			leftName:  "archive.zip",
			rightName: "archive.zip",
			expectTextDiff: `--- archive.zip
+++ archive.zip
├── readme.txt
│ @@ -1 +1 @@
│ -version 1.0
│ +version 2.0
`,
			expectJSONDiff: `{
  "source1": "archive.zip",
  "source2": "archive.zip",
  "details": [
    {
      "source1": "readme.txt",
      "source2": "readme.txt",
      "unified_diff": "@@ -1 +1 @@\n-version 1.0\n+version 2.0\n"
    }
  ]
}`,
		},
		{
			name:        "zip_with_file_added_in_middle",
			description: "ZIP with file added in middle should preserve original order",
			left: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "a_file.txt"}, Body: []byte("aaaaa")},
					{FileHeader: &zip.FileHeader{Name: "c_file.txt"}, Body: []byte("ccccc")},
				})
			},
			right: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "a_file.txt"}, Body: []byte("aaaaa")},
					{FileHeader: &zip.FileHeader{Name: "b_file.txt"}, Body: []byte("bbbbb")},
					{FileHeader: &zip.FileHeader{Name: "c_file.txt"}, Body: []byte("ccccc")},
				})
			},
			leftName:  "archive.zip",
			rightName: "archive.zip",
			expectTextDiff: `--- archive.zip
+++ archive.zip
├── file list
│ @@ -1,2 +1,3 @@
│  -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 a_file.txt
│ +-rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 b_file.txt
│  -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 c_file.txt
├── b_file.txt
│┄ Entry only in second archive
`,
			expectJSONDiff: `{
  "source1": "archive.zip",
  "source2": "archive.zip",
  "details": [
    {
      "source1": "file list",
      "source2": "file list",
      "unified_diff": "@@ -1,2 +1,3 @@\n -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 a_file.txt\n+-rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 b_file.txt\n -rw-rw-rw- Store    5            1979-11-30 00:00:00.000000 c_file.txt\n"
    },
    {
      "source1": "b_file.txt",
      "source2": "b_file.txt",
      "comments": ["Entry only in second archive"]
    }
  ]
}`,
		},
		{
			name:        "zip_with_files_reordered",
			description: "ZIP with same files in different order should show reordering comment",
			left: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "z_last.txt"}, Body: []byte("zzzzz")},
					{FileHeader: &zip.FileHeader{Name: "a_first.txt"}, Body: []byte("aaaaa")},
					{FileHeader: &zip.FileHeader{Name: "m_middle.txt"}, Body: []byte("mmmmm")},
				})
			},
			right: func() ([]byte, error) {
				return createZip([]archive.ZipEntry{
					{FileHeader: &zip.FileHeader{Name: "a_first.txt"}, Body: []byte("aaaaa")},
					{FileHeader: &zip.FileHeader{Name: "m_middle.txt"}, Body: []byte("mmmmm")},
					{FileHeader: &zip.FileHeader{Name: "z_last.txt"}, Body: []byte("zzzzz")},
				})
			},
			leftName:  "archive.zip",
			rightName: "archive.zip",
			expectTextDiff: `--- archive.zip
+++ archive.zip
│┄ Entry order differs (listings shown in sorted order)
`,
			expectJSONDiff: `{
  "source1": "archive.zip",
  "source2": "archive.zip",
  "comments": ["Entry order differs (listings shown in sorted order)"]
}`,
		},
		{
			name:        "gzip_different_compression_levels",
			description: "Gzip files with same content but different compression levels should report byte difference",
			left: func() ([]byte, error) {
				// Compress with compression level 1 (fastest)
				buf := new(bytes.Buffer)
				gw, err := gzip.NewWriterLevel(buf, gzip.BestSpeed)
				if err != nil {
					return nil, err
				}
				if _, err := gw.Write([]byte("hello world\n")); err != nil {
					return nil, err
				}
				if err := gw.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
			right: func() ([]byte, error) {
				// Compress with compression level 9 (best compression)
				buf := new(bytes.Buffer)
				gw, err := gzip.NewWriterLevel(buf, gzip.BestCompression)
				if err != nil {
					return nil, err
				}
				if _, err := gw.Write([]byte("hello world\n")); err != nil {
					return nil, err
				}
				if err := gw.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
			leftName:  "file.txt.gz",
			rightName: "file.txt.gz",
			// The decompressed content is identical, but the gzip bytes differ
			// This should trigger the fallback message
			expectTextDiff: `--- file.txt.gz
+++ file.txt.gz
│┄ Bytes differ but no semantic diff generated
`,
			expectJSONDiff: `{
  "source1": "file.txt.gz",
  "source2": "file.txt.gz",
  "comments": ["Bytes differ but no semantic diff generated"]
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
			leftFile := File{
				Name:   tc.leftName,
				Reader: bytes.NewReader(leftData),
			}
			rightFile := File{
				Name:   tc.rightName,
				Reader: bytes.NewReader(rightData),
			}
			t.Run("text_output", func(t *testing.T) {
				var buf bytes.Buffer
				opts := Options{Output: &buf}
				err := Diff(t.Context(), leftFile, rightFile, opts)
				if err != nil && err != ErrNoDiff {
					t.Fatalf("Diff failed: %v", err)
				}
				match := err == ErrNoDiff
				// Check match result
				if match != tc.expectMatch {
					t.Errorf("Expected match=%v, got %v", tc.expectMatch, match)
				}
				output := buf.String()
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
			t.Run("json_output", func(t *testing.T) {
				var buf bytes.Buffer
				opts := Options{OutputJSON: &buf}
				err := Diff(t.Context(), leftFile, rightFile, opts)
				if err != nil && err != ErrNoDiff {
					t.Fatalf("Diff failed: %v", err)
				}
				match := err == ErrNoDiff
				// Check match result
				if match != tc.expectMatch {
					t.Errorf("Expected match=%v, got %v", tc.expectMatch, match)
				}
				if tc.expectJSONDiff != "" {
					var expectedNode, actualNode any
					if err := json.Unmarshal([]byte(tc.expectJSONDiff), &expectedNode); err != nil {
						t.Fatalf("Failed to parse expected JSON: %v\nJSON: %s", err, tc.expectJSONDiff)
					}
					if err := json.Unmarshal(buf.Bytes(), &actualNode); err != nil {
						t.Fatalf("Failed to parse actual JSON: %v\nJSON: %s", err, buf.String())
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
			t.Run("output_node", func(t *testing.T) {
				var node DiffNode
				err := Diff(t.Context(), leftFile, rightFile, Options{OutputNode: &node})
				if err != nil && err != ErrNoDiff {
					t.Fatalf("Diff failed: %v", err)
				}
				match := err == ErrNoDiff
				// Check match result
				if match != tc.expectMatch {
					t.Errorf("Expected match=%v, got %v", tc.expectMatch, match)
				}
				// If we expect a diff, verify the node matches the JSON output
				if !tc.expectMatch && tc.expectJSONDiff != "" {
					var expectedNode DiffNode
					if err := json.Unmarshal([]byte(tc.expectJSONDiff), &expectedNode); err != nil {
						t.Fatalf("Failed to parse expected JSON: %v", err)
					}
					if diff := cmp.Diff(expectedNode, node); diff != "" {
						t.Errorf("OutputNode mismatch (-want +got):\n%s", diff)
					}
				}
			})
		})
	}
}

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

func createTgz(entries []archive.TarEntry) ([]byte, error) {
	tarData, err := createTar(entries)
	if err != nil {
		return nil, err
	}
	return createGzip(tarData)
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

func createZip(entries []archive.ZipEntry) ([]byte, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for _, entry := range entries {
		if err := entry.WriteTo(zw); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestMaxDepth(t *testing.T) {
	// Create a tar.gz with different content for testing
	leftData, err := createTgz([]archive.TarEntry{
		{Header: &tar.Header{Name: "file.txt", Size: 7, Mode: 0644}, Body: []byte("hello 1")},
	})
	if err != nil {
		t.Fatalf("Failed to create left tgz: %v", err)
	}
	rightData, err := createTgz([]archive.TarEntry{
		{Header: &tar.Header{Name: "file.txt", Size: 7, Mode: 0644}, Body: []byte("hello 2")},
	})
	if err != nil {
		t.Fatalf("Failed to create right tgz: %v", err)
	}
	testCases := []struct {
		name     string
		maxDepth int
		expect   string
	}{
		{
			name:     "unlimited depth",
			maxDepth: 0,
			expect: `--- archive.tar.gz
+++ archive.tar.gz
│   --- archive.tar
├─┐ +++ archive.tar
│ ├── file.txt
│ │ @@ -1 +1 @@
│ │ -hello 1
│ │ +hello 2
`,
		},
		{
			name:     "depth 1 stops at gzip",
			maxDepth: 1,
			// Should stop at gzip level, showing the tar as not expanded
			expect: `--- archive.tar.gz
+++ archive.tar.gz
├── archive.tar
│┄ Binary files differ
│┄ Archive not expanded (depth limit 1 reached)
`,
		},
		{
			name:     "depth 2 shows tar contents",
			maxDepth: 2,
			expect: `--- archive.tar.gz
+++ archive.tar.gz
│   --- archive.tar
├─┐ +++ archive.tar
│ ├── file.txt
│ │ @@ -1 +1 @@
│ │ -hello 1
│ │ +hello 2
`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			leftFile := File{
				Name:   "archive.tar.gz",
				Reader: bytes.NewReader(leftData),
			}
			rightFile := File{
				Name:   "archive.tar.gz",
				Reader: bytes.NewReader(rightData),
			}
			var buf bytes.Buffer
			opts := Options{Output: &buf, MaxDepth: tc.maxDepth}
			err := Diff(t.Context(), leftFile, rightFile, opts)
			if err != nil {
				t.Fatalf("Diff failed: %v", err)
			}
			output := buf.String()
			if output != tc.expect {
				t.Errorf("Text output mismatch\nExpected:\n%q\nGot:\n%q", tc.expect, output)
			}
		})
	}
}

func TestNodeStatus(t *testing.T) {
	testCases := []struct {
		name     string
		node     DiffNode
		expected NodeStatus
	}{
		{
			name:     "no comments returns StatusBoth",
			node:     DiffNode{Source1: "file.txt", Source2: "file.txt"},
			expected: StatusBoth,
		},
		{
			name: "unrelated comment returns StatusBoth",
			node: DiffNode{
				Source1:  "file.txt",
				Source2:  "file.txt",
				Comments: []string{"Binary files differ"},
			},
			expected: StatusBoth,
		},
		{
			name: "commentOnlyInFirst returns StatusOnlyFirst",
			node: DiffNode{
				Source1:  "file.txt",
				Source2:  "file.txt",
				Comments: []string{commentOnlyInFirst},
			},
			expected: StatusOnlyFirst,
		},
		{
			name: "commentOnlyInSecond returns StatusOnlySecond",
			node: DiffNode{
				Source1:  "file.txt",
				Source2:  "file.txt",
				Comments: []string{commentOnlyInSecond},
			},
			expected: StatusOnlySecond,
		},
		{
			name: "multiple comments with commentOnlyInFirst",
			node: DiffNode{
				Source1:  "file.txt",
				Source2:  "file.txt",
				Comments: []string{"Some other comment", commentOnlyInFirst},
			},
			expected: StatusOnlyFirst,
		},
		{
			name: "multiple comments with commentOnlyInSecond",
			node: DiffNode{
				Source1:  "file.txt",
				Source2:  "file.txt",
				Comments: []string{"Binary files differ", commentOnlyInSecond},
			},
			expected: StatusOnlySecond,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.node.Status()
			if got != tc.expected {
				t.Errorf("Expected Status() = %v, got %v", tc.expected, got)
			}
		})
	}
}
