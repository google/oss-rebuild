// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/google/oss-rebuild/pkg/diffr"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/pkg/errors"
)

// DiffSummary renders the file-level diff between the stabilized forms of the
// rebuilt artifact (from metadata) and the upstream artifact (fetched from
// upstreamURI), named "rebuild" and "upstream", so it shows what verification
// saw differ. It returns the empty string when they do not differ.
//
// Both artifacts are buffered in memory since archive readers seek, so this
// is for the mismatch path, not routine verification.
func DiffSummary(ctx context.Context, metadata rebuild.LocatableAssetStore, t rebuild.Target, upstreamURI string) (string, error) {
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return "", errors.Wrap(err, "getting stabilizers")
	}
	stab := func(dst io.Writer, src io.Reader) error {
		return stabilize.StabilizeWithOpts(dst, src, t.ArchiveType(), stabilize.StabilizeOpts{Stabilizers: stabilizers})
	}
	rd, err := metadata.Reader(ctx, rebuild.RebuildAsset.For(t))
	if err != nil {
		return "", errors.Wrap(err, "reading rebuild artifact")
	}
	defer checkClose(rd)
	var rebuilt bytes.Buffer
	if err := stab(&rebuilt, rd); err != nil {
		return "", errors.Wrap(err, "stabilizing rebuild artifact")
	}
	req, _ := http.NewRequest(http.MethodGet, upstreamURI, nil)
	resp, err := rebuild.DoContext(ctx, req)
	if err != nil {
		return "", errors.Wrap(err, "fetching upstream artifact")
	}
	defer checkClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", errors.Wrap(errors.New(resp.Status), "fetching upstream artifact")
	}
	var upstream bytes.Buffer
	if err := stab(&upstream, resp.Body); err != nil {
		return "", errors.Wrap(err, "stabilizing upstream artifact")
	}
	var summary strings.Builder
	err = diffr.Diff(ctx,
		diffr.File{Name: "rebuild", Reader: bytes.NewReader(rebuilt.Bytes())},
		diffr.File{Name: "upstream", Reader: bytes.NewReader(upstream.Bytes())},
		diffr.Options{OutputSummary: &summary})
	if errors.Is(err, diffr.ErrNoDiff) {
		return "", nil
	} else if err != nil {
		return "", errors.Wrap(err, "diffing artifacts")
	}
	return summary.String(), nil
}

// DiffSummaryPreamble introduces a DiffSummary to the agent: what the inputs
// are, how entries are grouped, and that entry paths are archive-relative.
const DiffSummaryPreamble = `File-level diff summary of the stabilized artifacts, "rebuild" being your rebuild and "upstream" the published artifact. Each archive is a "within <name>:" block grouping its entries as only in rebuild, only in upstream, and differ (present in both with different content), followed by the blocks of any nested archives. Paths are relative to the archive that contains them:`
