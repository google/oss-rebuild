// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package verifier provides a library for verifying and attesting to a rebuild.
package verifier

import (
	"context"
	"crypto"
	"io"
	"net/http"

	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// ArtifactSummary is a summary of an artifact for the purposes of verification.
type ArtifactSummary struct {
	URI            string
	Hash           hashext.MultiHash
	StabilizedHash hashext.MultiHash
}

// SummarizeArtifacts fetches and summarizes the rebuild and upstream artifacts.
func SummarizeArtifacts(ctx context.Context, metadata rebuild.LocatableAssetStore, t rebuild.Target, upstreamURI string, hashes []crypto.Hash) (rb, up ArtifactSummary, err error) {
	rb = ArtifactSummary{Hash: hashext.NewMultiHash(hashes...), StabilizedHash: hashext.NewMultiHash(hashes...)}
	up = ArtifactSummary{Hash: hashext.NewMultiHash(hashes...), StabilizedHash: hashext.NewMultiHash(hashes...), URI: upstreamURI}
	// Fetch and process rebuild.
	var r io.ReadCloser
	rbAsset := rebuild.RebuildAsset.For(t)
	rb.URI = metadata.URL(rbAsset).String()
	r, err = metadata.Reader(ctx, rbAsset)
	if err != nil {
		err = errors.Wrap(err, "reading artifact")
		return
	}
	defer checkClose(r)
	err = archive.Stabilize(rb.StabilizedHash, io.TeeReader(r, rb.Hash), t.ArchiveType())
	if err != nil {
		err = errors.Wrap(err, "fingerprinting rebuild")
		return
	}
	// Fetch and process upstream.
	req, _ := http.NewRequest(http.MethodGet, up.URI, nil)
	resp, err := rebuild.DoContext(ctx, req)
	if err != nil {
		err = errors.Wrap(err, "fetching upstream artifact")
		return
	}
	if resp.StatusCode != 200 {
		err = errors.Wrap(errors.New(resp.Status), "fetching upstream artifact")
		return
	}
	err = archive.Stabilize(up.StabilizedHash, io.TeeReader(resp.Body, up.Hash), t.ArchiveType())
	checkClose(resp.Body)
	if err != nil {
		err = errors.Wrap(err, "fingerprinting upstream")
		return
	}
	return
}
