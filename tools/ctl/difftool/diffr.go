// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package difftool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/google/oss-rebuild/pkg/diffr"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/pkg/errors"
)

// Diffr implements Differ using the diffr library.
type Diffr struct{}

func (d Diffr) AssetType() rebuild.AssetType {
	return DiffrAsset
}

func (d Diffr) Diff(ctx context.Context, rebuildPath, upstreamPath string, target rebuild.Target) ([]byte, error) {
	dir, err := os.MkdirTemp("", "*")
	if err != nil {
		return nil, errors.Wrap(err, "creating tempdir")
	}
	defer os.RemoveAll(dir)
	// Get stabilizers for this target
	stabilizers, err := stability.StabilizersForTarget(target)
	if err != nil {
		return nil, errors.Wrap(err, "getting stabilizers")
	}
	// TODO: We should use the version of Stabilize used in the rebuild.
	stabilizedRebuildPath := filepath.Join(dir, "stabilized-"+filepath.Base(rebuildPath))
	if err := stabilizeToFile(rebuildPath, stabilizedRebuildPath, target, stabilizers); err != nil {
		return nil, errors.Wrap(err, "stabilizing rebuild artifact")
	}
	// TODO: We should use the version of Stabilize used in the rebuild.
	stabilizedUpstreamPath := filepath.Join(dir, "stabilized-"+filepath.Base(upstreamPath))
	if err := stabilizeToFile(upstreamPath, stabilizedUpstreamPath, target, stabilizers); err != nil {
		return nil, errors.Wrap(err, "stabilizing upstream artifact")
	}
	// Open stabilized files for reading
	stabilizedRebuild, err := os.Open(stabilizedRebuildPath)
	if err != nil {
		return nil, errors.Wrap(err, "opening stabilized rebuild")
	}
	defer stabilizedRebuild.Close()
	stabilizedUpstream, err := os.Open(stabilizedUpstreamPath)
	if err != nil {
		return nil, errors.Wrap(err, "opening stabilized upstream")
	}
	defer stabilizedUpstream.Close()
	// Create output buffer
	var output bytes.Buffer
	// Run diffr
	maxDepth := target.ArchiveType().Layers()
	err = diffr.Diff(
		ctx,
		diffr.File{Name: rebuildPath, Reader: stabilizedRebuild},
		diffr.File{Name: upstreamPath, Reader: stabilizedUpstream},
		diffr.Options{MaxDepth: maxDepth, Output: &output},
	)
	if err != nil && !errors.Is(err, diffr.ErrNoDiff) {
		return nil, errors.Wrap(err, "running diffr")
	}
	return output.Bytes(), nil
}
