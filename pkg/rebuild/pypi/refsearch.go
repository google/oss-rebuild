// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
// given blobs, via gitscan.ExactTreeCount. Ties are ambiguous and rejected.
func matchArchiveBlobs(ctx context.Context, hashes []plumbing.Hash, pkg, version string, repo *git.Repository) (string, error) {
	closest, matched, total, err := gitscan.ExactTreeCount{}.Search(ctx, repo, hashes)
	if err != nil {
		return "", errors.Wrap(err, "searching trees for blob overlap")
	}
	if len(closest) != 1 {
		return "", errors.Errorf("ambiguous blob overlap [ties=%d,best=%d,total=%d]", len(closest), matched, total)
	}
	if _, err := repo.CommitObject(plumbing.NewHash(closest[0])); err != nil {
		return "", errors.Wrap(err, "resolving best-overlap commit")
	}
	ref := closest[0]
	log.Printf("archive-content match [pkg=%s,ver=%s,blobs=%d/%d,ref=%s]\n", pkg, version, matched, total, shortHash(ref))
	return ref, nil
}
