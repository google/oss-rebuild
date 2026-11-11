// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

func shortHash(h string) string {
	if len(h) > 9 {
		return h[:9]
	}
	return h
}

// archiveContentRef matches a release archive's file blobs against commit
// trees: an archive built from commit C contains C's file contents verbatim,
// so C's tree shares those blob hashes. It reads the pure wheel, else the
// sdist, which holds the most source files, else a platform wheel. Works
// without a declared version.
func archiveContentRef(ctx context.Context, mux rebuild.RegistryMux, pkg, version string, release *pypireg.Release, repo *git2.Repository) (string, error) {
	a, err := FindPureWheel(release.Artifacts)
	if err != nil {
		a, err = FindSourceDist(release.Artifacts)
	}
	if err != nil {
		a, err = FindPlatformWheel(release.Artifacts)
	}
	if err != nil {
		return "", errors.Wrap(err, "no release archive")
	}
	rc, err := mux.PyPI.Artifact(ctx, pkg, version, a.Filename)
	if err != nil {
		return "", errors.Wrap(err, "downloading archive")
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return "", errors.Wrap(err, "reading archive")
	}
	hashes, err := archiveBlobs(a.Filename, body)
	if err != nil {
		return "", errors.Wrap(err, "hashing archive contents")
	}
	return matchArchiveBlobs(ctx, hashes, pkg, version, a.UploadTime, repo)
}

// archiveBlobs returns the blob hashes of the files in a wheel or sdist, except
// a wheel's .dist-info, which the build generates, and empty files, which are
// often markers like py.typed and do not predict the commit.
func archiveBlobs(name string, body []byte) ([]plumbing.Hash, error) {
	var hashes []plumbing.Hash
	if strings.HasSuffix(name, ".tar.gz") {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if hashes, err = gitscan.BlobHashesFromTar(tar.NewReader(gz)); err != nil {
			return nil, err
		}
	} else {
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		zr.File = slices.DeleteFunc(zr.File, func(f *zip.File) bool { return strings.Contains(f.Name, ".dist-info/") })
		if hashes, err = gitscan.BlobHashesFromZip(zr); err != nil {
			return nil, err
		}
	}
	empty := plumbing.ComputeHash(plumbing.BlobObject, nil)
	return slices.DeleteFunc(hashes, func(h plumbing.Hash) bool { return h == empty }), nil
}

// matchArchiveBlobs returns the commit whose tree contains the most of the
// given blobs, via gitscan.ReachableTreeCount, as vetted and tie-broken by
// pickByDeclaredVersion for an archive uploaded at uploaded.
func matchArchiveBlobs(ctx context.Context, hashes []plumbing.Hash, pkg, version string, uploaded time.Time, repo *git2.Repository) (string, error) {
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
	pick := pickByDeclaredVersion(ctx, candidates, pkg, version, uploaded)
	if pick == nil {
		return "", errors.Errorf("no version-consistent blob overlap candidate [best=%d,total=%d,ties=%d]", matched, total, len(candidates))
	}
	ref := pick.Hash.String()
	log.Printf("archive-content match [pkg=%s,ver=%s,blobs=%d/%d,ties=%d,ref=%s]\n", pkg, version, matched, total, len(candidates), shortHash(ref))
	return ref, nil
}

// maxVettedTies bounds how many tied commits pickByDeclaredVersion reads, so
// that files left unchanged across a long history stay cheap to vet.
const maxVettedTies = 1000

// pickByDeclaredVersion chooses among commits tied on blob overlap by the version
// each one's build file declares, read where the previous candidate's build
// file was found and then anywhere in its tree. A build file confirming
// version keeps its commit in the running, one declaring none leaves it
// neutral, and one naming another version drops it. Commit time then orders
// the survivors: the latest confirming candidate, the version's final state,
// else the earliest neutral one, where the content was introduced, else nil.
// Spellings are compared under the approximate ordering, since PyPI
// canonicalizes them.
//
// Candidates authored over a day after uploaded are too late to have built the
// archive and are dropped, as author time survives rebases. The rest are read
// newest first, up to maxVettedTies, until one confirms version.
func pickByDeclaredVersion(ctx context.Context, candidates []*git2.Commit, pkg, version string, uploaded time.Time) *git2.Commit {
	if !uploaded.IsZero() {
		cutoff := uploaded.Add(24 * time.Hour)
		candidates = slices.DeleteFunc(candidates, func(c *git2.Commit) bool { return c.Author.When.After(cutoff) })
	}
	slices.SortStableFunc(candidates, func(a, b *git2.Commit) int { return byCommitTime(b, a) })
	var neutral *git2.Commit
	var dir string
	for _, c := range candidates[:min(len(candidates), maxVettedTies)] {
		var declared, found string
		if tree, err := c.Tree(); err == nil {
			declared, found = pypiresolver.FindDeclaredVersion(ctx, tree, dir, pkg)
		}
		dir = cmp.Or(found, dir)
		switch {
		case declared == "" && (neutral == nil || byCommitTime(c, neutral) < 0):
			neutral = c
		case declared != "" && pypiresolver.SameVersion(declared, version):
			return c
		}
	}
	return neutral
}

func byCommitTime(a, b *git2.Commit) int {
	return a.Committer.When.Compare(b.Committer.When)
}
