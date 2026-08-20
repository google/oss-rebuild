// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
)

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func wheelZip(t *testing.T, entries []archive.ZipEntry) []byte {
	t.Helper()
	buf, err := archivetest.ZipFile(entries)
	if err != nil {
		t.Fatalf("building wheel zip: %v", err)
	}
	return buf.Bytes()
}

func zipEntry(name, body string) archive.ZipEntry {
	return archive.ZipEntry{FileHeader: &zip.FileHeader{Name: name}, Body: []byte(body)}
}

func wheelHashes(t *testing.T, entries []archive.ZipEntry) []plumbing.Hash {
	t.Helper()
	whl := wheelZip(t, entries)
	zr := must(zip.NewReader(bytes.NewReader(whl), int64(len(whl))))
	hashes, err := gitscan.BlobHashesFromZip(zr)
	if err != nil {
		t.Fatalf("hashing wheel: %v", err)
	}
	return hashes
}

func TestMatchArchiveBlobs(t *testing.T) {
	const (
		coreV1 = "def core():\n    return 'core version one implementation body original'\n"
		coreV2 = "def core():\n    return 'core version two implementation body rewritten'\n"
		util   = "def util():\n    return 'util helper lorem ipsum dolor sit amet here'\n"
	)
	commits := []gitxtest.Commit{
		{ID: "v1", Files: gitxtest.FileContent{"acme/core.py": coreV1, "acme/util.py": util}},
		{ID: "v2", Parent: "v1", Files: gitxtest.FileContent{"acme/core.py": coreV2}},
	}
	repo := must(gitxtest.CreateRepo(commits, nil))

	// The wheel carries v2's sources plus generated dist-info metadata; the
	// metadata blobs are absent from the repo, so the scan drops them and the
	// v2 tree is the unique best match.
	hashes := wheelHashes(t, []archive.ZipEntry{
		zipEntry("acme/core.py", coreV2),
		zipEntry("acme/util.py", util),
		zipEntry("acme-2.0.0.dist-info/METADATA", "Metadata-Version: 2.1\nName: acme\nVersion: 2.0.0"),
	})
	ref, err := matchArchiveBlobs(context.Background(), hashes, "acme", "2.0.0", repo.Repository)
	if err != nil {
		t.Fatalf("matchArchiveBlobs(2.0.0): %v", err)
	}
	if got := repo.Commits["v2"].String(); ref != got {
		t.Errorf("matchArchiveBlobs(2.0.0) = %q, want v2 %q", ref, got)
	}

	// A commit differing only by a file the wheel does not carry ties with its
	// parent, which is ambiguous.
	tied := append(commits, gitxtest.Commit{ID: "v3", Parent: "v2", Files: gitxtest.FileContent{"README.md": "docs only\n"}})
	trepo := must(gitxtest.CreateRepo(tied, nil))
	if _, err := matchArchiveBlobs(context.Background(), hashes, "acme", "2.0.0", trepo.Repository); err == nil {
		t.Errorf("matchArchiveBlobs(tied trees) = nil error, want a rejection")
	}

	// A blob set absent from the repo entirely is rejected by the scan.
	if _, err := matchArchiveBlobs(context.Background(), []plumbing.Hash{plumbing.ZeroHash}, "acme", "2.0.0", repo.Repository); err == nil {
		t.Errorf("matchArchiveBlobs(no matching blobs) = nil error, want a rejection")
	}
}
