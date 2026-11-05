// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"regexp"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

// archiveContentRef matches the gem's packaged files against commit trees:
// a gem built from commit C packages C's file contents verbatim, so C's tree
// shares those blob hashes. Works for versions that no tag names.
func archiveContentRef(ctx context.Context, gem []byte, pkg, version string, repo *git2.Repository) (string, error) {
	hashes, err := dataBlobHashes(gem)
	if err != nil {
		return "", errors.Wrap(err, "hashing gem contents")
	}
	return matchArchiveBlobs(ctx, hashes, pkg, version, repo)
}

// dataBlobHashes returns the git blob hashes of the files in a .gem's
// data.tar.gz.
func dataBlobHashes(gem []byte) ([]plumbing.Hash, error) {
	tr := tar.NewReader(bytes.NewReader(gem))
	for h, err := range iterx.ToSeq2(tr, io.EOF) {
		if err != nil {
			return nil, errors.Wrap(err, "reading gem tar")
		}
		if h.Name != "data.tar.gz" {
			continue
		}
		gz, err := gzip.NewReader(tr)
		if err != nil {
			return nil, errors.Wrap(err, "decompressing data.tar.gz")
		}
		defer gz.Close()
		return gitscan.BlobHashesFromTar(tar.NewReader(gz))
	}
	return nil, errors.New("data.tar.gz not found in gem")
}

// matchArchiveBlobs returns the commit whose tree contains the most of the
// given blobs, via gitscan.ReachableTreeCount, as vetted and tie-broken by
// pickByDeclaredVersion.
func matchArchiveBlobs(ctx context.Context, hashes []plumbing.Hash, pkg, version string, repo *git2.Repository) (string, error) {
	blobs := make([]git2.Hash, len(hashes))
	for i, h := range hashes {
		blobs[i] = git2.Hash(h)
	}
	closest, matched, total, err := gitscan.ReachableTreeCount{}.Search(ctx, repo, blobs)
	if err != nil {
		return "", errors.Wrap(err, "searching trees for blob overlap")
	}
	candidates := make([]*git2.Commit, 0, len(closest))
	for _, h := range closest {
		c, err := repo.CommitObject(git2.NewHash(h))
		if err != nil {
			return "", errors.Wrap(err, "resolving best-overlap commit")
		}
		candidates = append(candidates, c)
	}
	pick := pickByDeclaredVersion(repo, candidates, version)
	if pick == nil {
		return "", errors.Errorf("no version-consistent blob overlap candidate [best=%d,total=%d,ties=%d]", matched, total, len(candidates))
	}
	ref := pick.Hash.String()
	log.Printf("archive-content match [pkg=%s,ver=%s,blobs=%d/%d,ties=%d,ref=%s]\n", pkg, version, matched, total, len(candidates), ref[:9])
	return ref, nil
}

var (
	// versionFiles are where gems conventionally declare their version.
	versionFiles = []*regexp.Regexp{regexp.MustCompile(`(^|/)lib/.*version\.rb$`), regexp.MustCompile(`\.gemspec$`)}
	// declaredVersion matches `VERSION = "1.2.3"` and `s.version = "1.2.3"`.
	declaredVersion = regexp.MustCompile(`(?:\bVERSION|\.version)\s*=\s*['"]([^'"]+)['"]`)
)

// pickByDeclaredVersion breaks a tie among equally matching commits using the
// version each one declares in version.rb or a gemspec. It returns the latest
// commit that declares version. Failing that, it returns the earliest commit
// that declares no version, which is where the matched files first appeared.
// Commits that declare only other versions are never returned. A monorepo
// declares a version per gem, so one matching declaration is enough.
func pickByDeclaredVersion(repo *git2.Repository, candidates []*git2.Commit, version string) *git2.Commit {
	var confirming, neutral []*git2.Commit
	for _, c := range candidates {
		grs, err := repo.Grep(&git2.GrepOptions{CommitHash: c.Hash, PathSpecs: versionFiles, Patterns: []*regexp.Regexp{declaredVersion}})
		if err != nil || len(grs) == 0 {
			neutral = append(neutral, c)
			continue
		}
		if slices.ContainsFunc(grs, func(gr git2.GrepResult) bool {
			m := declaredVersion.FindStringSubmatch(gr.Content)
			return m != nil && m[1] == version
		}) {
			confirming = append(confirming, c)
		}
	}
	switch {
	case len(confirming) > 0:
		return slices.MaxFunc(confirming, byCommitTime)
	case len(neutral) > 0:
		return slices.MinFunc(neutral, byCommitTime)
	}
	return nil
}

func byCommitTime(a, b *git2.Commit) int {
	return a.Committer.When.Compare(b.Committer.When)
}
