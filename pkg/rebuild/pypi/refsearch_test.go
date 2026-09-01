// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
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

func pyprojectTOML(name, version string) string {
	return fmt.Sprintf("[project]\nname = \"%s\"\nversion = \"%s\"\n", name, version)
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

	// Wheels carry no build files, so commits differing only in the declared
	// version tie on blobs; the build file at each candidate breaks the tie.
	verTie := []gitxtest.Commit{
		{ID: "r1", Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "1.0.0"), "acme/core.py": coreV2, "acme/util.py": util}},
		{ID: "r2", Parent: "r1", Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "2.0.0")}},
	}
	vrepo := must(gitxtest.CreateRepo(verTie, nil))
	ref, err = matchArchiveBlobs(context.Background(), hashes, "acme", "2.0.0", vrepo.Repository)
	if err != nil {
		t.Fatalf("matchArchiveBlobs(version tie, 2.0.0): %v", err)
	}
	if got := vrepo.Commits["r2"].String(); ref != got {
		t.Errorf("matchArchiveBlobs(version tie, 2.0.0) = %q, want r2 %q", ref, got)
	}
	ref, err = matchArchiveBlobs(context.Background(), hashes, "acme", "1.0.0", vrepo.Repository)
	if err != nil {
		t.Fatalf("matchArchiveBlobs(version tie, 1.0.0): %v", err)
	}
	if got := vrepo.Commits["r1"].String(); ref != got {
		t.Errorf("matchArchiveBlobs(version tie, 1.0.0) = %q, want r1 %q", ref, got)
	}

	// A version no tied commit declares yields no match rather than a wrong one.
	if _, err := matchArchiveBlobs(context.Background(), hashes, "acme", "3.0.0", vrepo.Repository); err == nil {
		t.Errorf("matchArchiveBlobs(version tie, 3.0.0) = nil error, want a rejection")
	}

	// Ties among version-silent candidates resolve to the earliest, where the
	// artifact's content was introduced.
	silent := []gitxtest.Commit{
		{ID: "s1", Files: gitxtest.FileContent{"pyproject.toml": "[project]\nname = \"acme\"\ndynamic = [\"version\"]\n", "acme/core.py": coreV2, "acme/util.py": util}},
		{ID: "s2", Parent: "s1", Files: gitxtest.FileContent{"README.md": "doc change\n"}},
	}
	srepo := must(gitxtest.CreateRepo(silent, nil))
	ref, err = matchArchiveBlobs(context.Background(), hashes, "acme", "2.0.0", srepo.Repository)
	if err != nil {
		t.Fatalf("matchArchiveBlobs(silent tie): %v", err)
	}
	if s1, s2 := srepo.Commits["s1"].String(), srepo.Commits["s2"].String(); ref != s1 && ref != s2 {
		t.Errorf("matchArchiveBlobs(silent tie) = %q, want s1 %q (or s2 %q on equal timestamps)", ref, s1, s2)
	}

	// A blob set absent from the repo entirely is rejected by the scan.
	if _, err := matchArchiveBlobs(context.Background(), []plumbing.Hash{plumbing.ZeroHash}, "acme", "2.0.0", repo.Repository); err == nil {
		t.Errorf("matchArchiveBlobs(no matching blobs) = nil error, want a rejection")
	}
}
