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

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/versionx"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
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
func archiveContentRef(ctx context.Context, mux rebuild.RegistryMux, pkg, version string, release *pypireg.Release, repo *git.Repository) (string, error) {
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
// given blobs, via gitscan.ExactTreeCount. pickArchiveCommit resolves ties by
// declared version.
func matchArchiveBlobs(ctx context.Context, hashes []plumbing.Hash, pkg, version string, repo *git.Repository) (string, error) {
	closest, matched, total, err := gitscan.ExactTreeCount{}.Search(ctx, repo, hashes)
	if err != nil {
		return "", errors.Wrap(err, "searching trees for blob overlap")
	}
	best := make([]*object.Commit, 0, len(closest))
	for _, h := range closest {
		if c, err := repo.CommitObject(plumbing.NewHash(h)); err == nil {
			best = append(best, c)
		}
	}
	ref := pickArchiveCommit(ctx, best, pkg, version)
	if ref == "" {
		return "", errors.Errorf("no version-consistent blob overlap candidate [best=%d,total=%d]", matched, total)
	}
	log.Printf("archive-content match [pkg=%s,ver=%s,blobs=%d/%d,ref=%s]\n", pkg, version, matched, total, shortHash(ref))
	return ref, nil
}

// pickArchiveCommit breaks a blob-overlap tie by declared version: the most
// recent commit whose build file confirms version, else the earliest that
// declares none (where the content was introduced), else "". A build file
// naming another version drops its commit. Spellings are compared under the
// approximate ordering, since PyPI canonicalizes them. Each candidate's build
// file is looked for where the previous candidate's was, then anywhere.
func pickArchiveCommit(ctx context.Context, best []*object.Commit, pkg, version string) string {
	var confirmed, neutral []*object.Commit
	var dir string
	for _, c := range best {
		tree, err := c.Tree()
		if err != nil {
			continue
		}
		declared, found := pypiresolver.FindDeclaredVersion(ctx, tree, dir, pkg)
		dir = cmp.Or(found, dir)
		switch {
		case declared == "":
			neutral = append(neutral, c)
		case versionx.ApproxCompare(declared, version) == 0:
			confirmed = append(confirmed, c)
		}
	}
	switch {
	case len(confirmed) > 0:
		return slices.MaxFunc(confirmed, byCommitTime).Hash.String()
	case len(neutral) > 0:
		return slices.MinFunc(neutral, byCommitTime).Hash.String()
	}
	return ""
}

func byCommitTime(a, b *object.Commit) int {
	return a.Committer.When.Compare(b.Committer.When)
}
