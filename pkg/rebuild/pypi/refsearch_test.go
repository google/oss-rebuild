// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

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
	day := func(n int) time.Time { return time.Date(2024, time.January, n, 0, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	// The wheel carries v2's sources plus generated dist-info metadata; the
	// metadata blobs are absent from the repo, so the scan drops them.
	hashes := wheelHashes(t, []archive.ZipEntry{
		zipEntry("acme/core.py", coreV2),
		zipEntry("acme/util.py", util),
		zipEntry("acme-2.0.0.dist-info/METADATA", "Metadata-Version: 2.1\nName: acme\nVersion: 2.0.0"),
	})
	for _, tc := range []struct {
		name    string
		commits []gitxtest.Commit
		version string
		want    string // commit ID, or "" for a rejection
	}{
		{
			name: "unique best overlap",
			commits: []gitxtest.Commit{
				{ID: "v1", Time: day(1), Files: gitxtest.FileContent{"acme/core.py": coreV1, "acme/util.py": util}},
				{ID: "v2", Time: day(2), Parent: "v1", Files: gitxtest.FileContent{"acme/core.py": coreV2}},
			},
			version: "2.0.0",
			want:    "v2",
		},
		{
			// A build file naming another version drops its commit, even the
			// only one carrying the content.
			name: "unique best overlap declaring another version",
			commits: []gitxtest.Commit{
				{ID: "v1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "0.0.0"), "acme/core.py": coreV1, "acme/util.py": util}},
				{ID: "v2", Time: day(2), Parent: "v1", Files: gitxtest.FileContent{"acme/core.py": coreV2}},
			},
			version: "2.0.0",
			want:    "",
		},
		{
			// Wheels carry no build files, so commits differing only in the
			// declared version tie on blobs; the latest confirming commit wins.
			name: "version tie, latest confirming",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "1.0.0"), "acme/core.py": coreV2, "acme/util.py": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "2.0.0")}},
				{ID: "r3", Time: day(3), Parent: "r2", Files: gitxtest.FileContent{"README.md": "doc change\n"}},
			},
			version: "2.0.0",
			want:    "r3",
		},
		{
			name: "version tie, earlier confirming",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "1.0.0"), "acme/core.py": coreV2, "acme/util.py": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "2.0.0")}},
			},
			version: "1.0.0",
			want:    "r1",
		},
		{
			// A version no tied commit declares yields no match rather than a
			// wrong one.
			name: "version tie, none confirming",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "1.0.0"), "acme/core.py": coreV2, "acme/util.py": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "2.0.0")}},
			},
			version: "3.0.0",
			want:    "",
		},
		{
			name: "version tie, neutral over contradicting",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": pyprojectTOML("acme", "1.0.0"), "acme/core.py": coreV2, "acme/util.py": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"pyproject.toml": "[project]\nname = \"acme\"\ndynamic = [\"version\"]\n"}},
			},
			version: "3.0.0",
			want:    "r2",
		},
		{
			// Ties among version-silent candidates resolve to the earliest,
			// where the artifact's content was introduced.
			name: "silent tie, earliest",
			commits: []gitxtest.Commit{
				{ID: "s1", Time: day(1), Files: gitxtest.FileContent{"pyproject.toml": "[project]\nname = \"acme\"\ndynamic = [\"version\"]\n", "acme/core.py": coreV2, "acme/util.py": util}},
				{ID: "s2", Time: day(2), Parent: "s1", Files: gitxtest.FileContent{"README.md": "doc change\n"}},
			},
			version: "2.0.0",
			want:    "s1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo(tc.commits, nil))
			ref, err := matchArchiveBlobs(ctx, hashes, "acme", tc.version, repo.Repository)
			if tc.want == "" {
				if err == nil {
					t.Errorf("matchArchiveBlobs = %q, nil error, want a rejection", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchArchiveBlobs: %v", err)
			}
			if want := repo.Commits[tc.want].String(); ref != want {
				t.Errorf("matchArchiveBlobs = %q, want %s %q", ref, tc.want, want)
			}
		})
	}

	// A blob set absent from the repo entirely is rejected by the scan.
	repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "v1", Files: gitxtest.FileContent{"acme/core.py": coreV1}}}, nil))
	if _, err := matchArchiveBlobs(ctx, []plumbing.Hash{plumbing.ZeroHash}, "acme", "2.0.0", repo.Repository); err == nil {
		t.Errorf("matchArchiveBlobs(no matching blobs) = nil error, want a rejection")
	}
}
