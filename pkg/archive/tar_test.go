// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"bytes"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
)

// buildTarWithSymlink creates a tar reader containing a single symlink entry.
func buildTarWithSymlink(name, linkname string) *tar.Reader {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	_ = tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     name,
		Linkname: linkname,
	})
	_ = tw.Close()
	return tar.NewReader(bytes.NewReader(buf.Bytes()))
}

// TestExtractTarSymlinkTraversal verifies that symlink entries whose destination
// path or target escapes the extraction root are silently skipped rather than
// written to the filesystem.
func TestExtractTarSymlinkTraversal(t *testing.T) {
	tests := []struct {
		desc     string
		name     string
		linkname string
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
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			fs := memfs.New()
			tr := buildTarWithSymlink(tc.name, tc.linkname)
			if err := ExtractTar(tr, fs, ExtractOptions{SubDir: "."}); err != nil {
				t.Fatalf("ExtractTar() returned unexpected error: %v", err)
			}
			// Verify that no symlink was created inside the memfs root.
			entries, _ := fs.ReadDir(filepath.Clean("."))
			if len(entries) != 0 {
				t.Errorf("ExtractTar() created %d unexpected entries in root: %v", len(entries), entries)
			}
		})
	}
}

// TestExtractTarSymlinkSafe verifies that a well-formed symlink (both name and
// target within the extraction root) is extracted successfully.
func TestExtractTarSymlinkSafe(t *testing.T) {
	fs := memfs.New()
	tr := buildTarWithSymlink("mylink", "target_file")
	if err := ExtractTar(tr, fs, ExtractOptions{SubDir: "."}); err != nil {
		t.Fatalf("ExtractTar() returned unexpected error: %v", err)
	}
	entries, err := fs.ReadDir(filepath.Clean("."))
	if err != nil {
		t.Fatalf("ReadDir() error: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("ExtractTar() created %d entries, want 1", len(entries))
	}
}
