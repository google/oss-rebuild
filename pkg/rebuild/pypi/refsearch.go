// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"io"
	"log"
	"slices"

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

// archiveContentRef matches the pure wheel's file blobs against commit trees:
// a wheel built from commit C contains C's file contents verbatim, so C's tree
// shares those blob hashes. Works without a declared version.
func archiveContentRef(ctx context.Context, mux rebuild.RegistryMux, pkg, version string, release *pypireg.Release, repo *git2.Repository) (string, error) {
	wheel, err := FindPureWheel(release.Artifacts)
	if err != nil {
		return "", errors.Wrap(err, "no pure wheel")
	}
	rc, err := mux.PyPI.Artifact(ctx, pkg, version, wheel.Filename)
	if err != nil {
		return "", errors.Wrap(err, "downloading wheel")
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return "", errors.Wrap(err, "reading wheel")
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", errors.Wrap(err, "opening wheel zip")
	}
	hashes, err := gitscan.BlobHashesFromZip(zr)
	if err != nil {
		return "", errors.Wrap(err, "hashing wheel contents")
	}
	return matchArchiveBlobs(ctx, hashes, pkg, version, repo)
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
	pick := pickByDeclaredVersion(ctx, candidates, pkg, version)
	if pick == nil {
		return "", errors.Errorf("no version-consistent blob overlap candidate [best=%d,total=%d,ties=%d]", matched, total, len(candidates))
	}
	ref := pick.Hash.String()
	log.Printf("archive-content match [pkg=%s,ver=%s,blobs=%d/%d,ties=%d,ref=%s]\n", pkg, version, matched, total, len(candidates), shortHash(ref))
	return ref, nil
}

// pickByDeclaredVersion chooses among commits tied on blob overlap by the version
// each one's build file declares, read where the previous candidate's build
// file was found and then anywhere in its tree. A build file confirming
// version keeps its commit in the running, one declaring none leaves it
// neutral, and one naming another version drops it. Commit time then orders
// the survivors: the latest confirming candidate, the version's final state,
// else the earliest neutral one, where the content was introduced, else nil.
// Spellings are compared under the approximate ordering, since PyPI
// canonicalizes them.
func pickByDeclaredVersion(ctx context.Context, candidates []*git2.Commit, pkg, version string) *git2.Commit {
	var confirming, neutral []*git2.Commit
	var dir string
	for _, c := range candidates {
		var declared, found string
		if tree, err := c.Tree(); err == nil {
			declared, found = pypiresolver.FindDeclaredVersion(ctx, tree, dir, pkg)
		}
		dir = cmp.Or(found, dir)
		switch {
		case declared == "":
			neutral = append(neutral, c)
		case pypiresolver.SameVersion(declared, version):
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
