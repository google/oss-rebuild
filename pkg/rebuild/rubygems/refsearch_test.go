// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

func tarEntry(name, body string) archive.TarEntry {
	return archive.TarEntry{Header: &tar.Header{Name: name, Mode: 0644, Typeflag: tar.TypeReg}, Body: []byte(body)}
}

// gemFile assembles a .gem: an outer tar holding a gzipped metadata
// document and data.tar.gz with the packaged files.
func gemFile(files map[string]string) []byte {
	var entries []archive.TarEntry
	for name, body := range files {
		entries = append(entries, tarEntry(name, body))
	}
	data := must(archivetest.TgzFile(entries))
	meta := must(archivetest.GzFile([]byte("--- !ruby/object:Gem::Specification\nname: acme\n"), gzip.Header{}))
	return must(archivetest.TarFile([]archive.TarEntry{
		tarEntry("metadata.gz", meta.String()),
		tarEntry("data.tar.gz", data.String()),
	})).Bytes()
}

func versionRB(v string) string {
	return "module Acme\n  VERSION = \"" + v + "\"\nend\n"
}

func TestArchiveContentRef(t *testing.T) {
	const (
		coreV1 = "module Acme\n  def self.core = 'core version one implementation body'\nend\n"
		coreV2 = "module Acme\n  def self.core = 'core version two implementation body'\nend\n"
		util   = "module Acme\n  def self.util = 'util helper lorem ipsum dolor sit amet'\nend\n"
	)
	day := func(n int) time.Time { return time.Date(2024, time.January, n, 0, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	gem := gemFile(map[string]string{"lib/acme/core.rb": coreV2, "lib/acme/util.rb": util})
	for _, tc := range []struct {
		name    string
		commits []gitxtest.Commit
		version string
		want    string // commit ID, or "" for a rejection
	}{
		{
			name: "unique best overlap",
			commits: []gitxtest.Commit{
				{ID: "v1", Time: day(1), Files: gitxtest.FileContent{"lib/acme/core.rb": coreV1, "lib/acme/util.rb": util}},
				{ID: "v2", Time: day(2), Parent: "v1", Files: gitxtest.FileContent{"lib/acme/core.rb": coreV2}},
			},
			version: "2.0.0",
			want:    "v2",
		},
		{
			// version.rb is not packaged, so commits differing in it tie.
			name: "version tie, latest confirming",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"lib/acme/version.rb": versionRB("1.0.0"), "lib/acme/core.rb": coreV2, "lib/acme/util.rb": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"lib/acme/version.rb": versionRB("2.0.0")}},
				{ID: "r3", Time: day(3), Parent: "r2", Files: gitxtest.FileContent{"README.md": "doc change\n"}},
			},
			version: "2.0.0",
			want:    "r3",
		},
		{
			name: "version tie, none confirming",
			commits: []gitxtest.Commit{
				{ID: "r1", Time: day(1), Files: gitxtest.FileContent{"lib/acme/version.rb": versionRB("1.0.0"), "lib/acme/core.rb": coreV2, "lib/acme/util.rb": util}},
				{ID: "r2", Time: day(2), Parent: "r1", Files: gitxtest.FileContent{"lib/acme/version.rb": versionRB("2.0.0")}},
			},
			version: "3.0.0",
			want:    "",
		},
		{
			name: "gemspec declaration confirms",
			commits: []gitxtest.Commit{
				{ID: "g1", Time: day(1), Files: gitxtest.FileContent{"acme.gemspec": "Gem::Specification.new do |s|\n  s.version = '1.0.0'\nend\n", "lib/acme/core.rb": coreV2, "lib/acme/util.rb": util}},
				{ID: "g2", Time: day(2), Parent: "g1", Files: gitxtest.FileContent{"acme.gemspec": "Gem::Specification.new do |s|\n  s.version = '2.0.0'\nend\n"}},
			},
			version: "1.0.0",
			want:    "g1",
		},
		{
			// The earliest silent commit introduced the gem's content.
			name: "silent tie, earliest",
			commits: []gitxtest.Commit{
				{ID: "s1", Time: day(1), Files: gitxtest.FileContent{"lib/acme/core.rb": coreV2, "lib/acme/util.rb": util}},
				{ID: "s2", Time: day(2), Parent: "s1", Files: gitxtest.FileContent{"README.md": "doc change\n"}},
			},
			version: "2.0.0",
			want:    "s1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepo(tc.commits, nil))
			ref, err := archiveContentRef(ctx, gem, "acme", tc.version, repo.Repository)
			if tc.want == "" {
				if err == nil {
					t.Errorf("archiveContentRef = %q, nil error, want a rejection", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("archiveContentRef: %v", err)
			}
			if want := repo.Commits[tc.want].String(); ref != want {
				t.Errorf("archiveContentRef = %q, want %s %q", ref, tc.want, want)
			}
		})
	}

	// A gem whose contents appear nowhere in the repo is rejected.
	repo := must(gitxtest.CreateRepo([]gitxtest.Commit{{ID: "v1", Files: gitxtest.FileContent{"lib/acme/core.rb": coreV1}}}, nil))
	if _, err := archiveContentRef(ctx, gemFile(map[string]string{"lib/acme/other.rb": "unrelated\n"}), "acme", "2.0.0", repo.Repository); err == nil {
		t.Errorf("archiveContentRef(no matching blobs) = nil error, want a rejection")
	}
}
