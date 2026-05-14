// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive_test

import (
	"archive/tar"
	"bytes"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

// TestExtractTarSymlink verifies that symlink entries whose destination path
// or target escapes the extraction root are silently skipped, while
// well-formed symlinks within the root are extracted.
func TestExtractTarSymlink(t *testing.T) {
	tests := []struct {
		desc        string
		name        string
		linkname    string
		wantEntries int
	}{
		{
			desc:     "symlink destination escapes via dotdot",
			name:     "../../evil",
			linkname: "safe_target",
		},
		{
			desc:     "symlink target escapes via dotdot",
			name:     "safe_link",
			linkname: "../../../../etc/passwd",
		},
		{
			desc:     "symlink target is absolute path",
			name:     "safe_link2",
			linkname: "/etc/passwd",
		},
		{
			desc:     "both name and target escape",
			name:     "../../link",
			linkname: "../../../../etc/shadow",
		},
		{
			desc:        "well-formed symlink within root",
			name:        "mylink",
			linkname:    "target_file",
			wantEntries: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			buf, err := archivetest.TarFile([]archive.TarEntry{{
				Header: &tar.Header{
					Typeflag: tar.TypeSymlink,
					Name:     tc.name,
					Linkname: tc.linkname,
				},
			}})
			if err != nil {
				t.Fatalf("TarFile() error: %v", err)
			}
			fs := memfs.New()
			tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
			if err := archive.ExtractTar(tr, fs, archive.ExtractOptions{SubDir: "."}); err != nil {
				t.Fatalf("ExtractTar() returned unexpected error: %v", err)
			}
			entries, _ := fs.ReadDir(filepath.Clean("."))
			if len(entries) != tc.wantEntries {
				t.Errorf("ExtractTar() created %d entries, want %d: %v", len(entries), tc.wantEntries, entries)
			}
		})
	}
}
