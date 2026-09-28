// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func TestSummary(t *testing.T) {
	tarEntry := func(name, body string, mtime int64) archive.TarEntry {
		return archive.TarEntry{Header: &tar.Header{Name: name, Mode: 0644, ModTime: time.Unix(mtime, 0)}, Body: []byte(body)}
	}
	zipEntry := func(name string, body []byte) archive.ZipEntry {
		return archive.ZipEntry{FileHeader: &zip.FileHeader{Name: name}, Body: body}
	}
	tgz := func(entries ...archive.TarEntry) []byte { return must(archivetest.TgzFile(entries)).Bytes() }
	tarb := func(entries ...archive.TarEntry) []byte { return must(archivetest.TarFile(entries)).Bytes() }
	zipb := func(entries ...archive.ZipEntry) []byte { return must(archivetest.ZipFile(entries)).Bytes() }
	gz := func(body string) []byte { return must(archivetest.GzFile([]byte(body), gzip.Header{})).Bytes() }
	testCases := []struct {
		name                string
		leftName, rightName string
		left, right         []byte
		want                string
	}{
		{
			name:      "TextDiffersAsWhole",
			leftName:  "file.txt",
			rightName: "file.txt",
			left:      []byte("version 1.0\n"),
			right:     []byte("version 2.0\n"),
			want: `file.txt vs file.txt differ as a whole
`,
		},
		{
			name:      "TypesDifferAsWhole",
			leftName:  "file.txt",
			rightName: "file.bin",
			left:      []byte("hello world"),
			right:     []byte{0x00, 0x01, 0x02, 0x03, 0x00},
			want: `file.txt vs file.bin differ as a whole (File types differ: text vs binary)
`,
		},
		{
			name:      "CompressedText",
			leftName:  "notes.txt.gz",
			rightName: "notes.txt.gz",
			left:      gz("version 1.0\n"),
			right:     gz("version 2.0\n"),
			want: `notes.txt.gz vs notes.txt.gz differ as a whole
`,
		},
		{
			// Named as the agent's --label names them, with no suffix on
			// either. Decompression layers add no block of their own.
			name:      "NestedArchives",
			leftName:  "rebuild",
			rightName: "upstream",
			left: tgz(
				tarEntry("package/index.js", "module.exports = 1\n", 1000),
				tarEntry("package/changed.txt", "version 1.0\n", 1000),
				tarEntry("package/removed.txt", "gone\n", 1000),
				tarEntry("package/native.node", "\x7fELF\x01\x02\x03", 1000),
				tarEntry("package/vendor.tar.gz", string(tgz(tarEntry("deep.txt", "version 1.0\n", 1000))), 1000),
			),
			right: tgz(
				tarEntry("package/index.js", "module.exports = 1\n", 1000),
				tarEntry("package/changed.txt", "version 2.0\n", 1000),
				tarEntry("package/added.txt", "new\n", 1000),
				tarEntry("package/native.node", "\x7fELF\x01\x02\x09", 1000),
				tarEntry("package/vendor.tar.gz", string(tgz(tarEntry("deep.txt", "version 2.0\n", 1000))), 1000),
			),
			want: `within rebuild (vs upstream):
  only in rebuild (1):
    package/removed.txt
  only in upstream (1):
    package/added.txt
  differ (2):
    package/changed.txt
    package/native.node (Binary files differ)
  within package/vendor.tar.gz:
    differ (1):
      deep.txt
`,
		},
		{
			// A compressed entry is one entry, however its content differs.
			name:      "CompressedEntry",
			leftName:  "archive.tar",
			rightName: "archive.tar",
			left:      tarb(tarEntry("notes.txt.gz", string(gz("version 1.0\n")), 1000)),
			right:     tarb(tarEntry("notes.txt.gz", string(gz("version 2.0\n")), 1000)),
			want: `within archive.tar:
  differ (1):
    notes.txt.gz
`,
		},
		{
			name:      "ListingOnly",
			leftName:  "archive.tar",
			rightName: "archive.tar",
			left:      tarb(tarEntry("file.txt", "same\n", 1000)),
			right:     tarb(tarEntry("file.txt", "same\n", 2000)),
			want: `within archive.tar:
  listing differs (entry metadata only)
`,
		},
		{
			name:      "NestedListingOnly",
			leftName:  "outer.zip",
			rightName: "outer.zip",
			left: zipb(
				zipEntry("top.txt", []byte("version 1.0\n")),
				zipEntry("vendor/inner.tar", tarb(tarEntry("file.txt", "same\n", 1000))),
			),
			right: zipb(
				zipEntry("top.txt", []byte("version 2.0\n")),
				zipEntry("vendor/inner.tar", tarb(tarEntry("file.txt", "same\n", 2000))),
			),
			want: `within outer.zip:
  differ (1):
    top.txt
  within vendor/inner.tar:
    listing differs (entry metadata only)
`,
		},
		{
			// A nested archive that grows changes the outer listing too, which
			// is not a metadata-only difference.
			name:      "NestedContentOnly",
			leftName:  "archive.tar",
			rightName: "archive.tar",
			left:      tarb(tarEntry("vendor.tar.gz", string(tgz(tarEntry("deep.txt", "version 1.0\n", 1000))), 1000)),
			right:     tarb(tarEntry("vendor.tar.gz", string(tgz(tarEntry("deep.txt", "version 2.0 and then some more text\n", 1000))), 1000)),
			want: `within archive.tar:
  within vendor.tar.gz:
    differ (1):
      deep.txt
`,
		},
		{
			name:      "UnmatchedDuplicateEntry",
			leftName:  "archive.tar",
			rightName: "archive.tar",
			left:      tarb(tarEntry("dup.txt", "same\n", 1000), tarEntry("dup.txt", "again\n", 1000)),
			right:     tarb(tarEntry("dup.txt", "same\n", 1000)),
			want: `within archive.tar:
  only in archive.tar (1):
    dup.txt [occurrence 2] (Unmatched duplicate entry)
`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			left := File{Name: tc.leftName, Reader: bytes.NewReader(tc.left)}
			right := File{Name: tc.rightName, Reader: bytes.NewReader(tc.right)}
			if err := Diff(t.Context(), left, right, Options{OutputSummary: &buf}); err != nil {
				t.Fatalf("Diff() = %v", err)
			}
			if diff := cmp.Diff(tc.want, buf.String()); diff != "" {
				t.Errorf("Summary mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
