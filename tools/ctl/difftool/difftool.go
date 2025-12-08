// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package difftool

import (
	"context"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

const (
	// DiffoscopeAsset is the asset type for diffoscope diffs.
	DiffoscopeAsset rebuild.AssetType = "diff"
	// DiffrAsset is the asset type for diffr diffs.
	DiffrAsset rebuild.AssetType = "diffr"
)

// Differ compares two artifacts and returns diff output.
type Differ interface {
	Diff(ctx context.Context, rebuildPath, upstreamPath string, target rebuild.Target) ([]byte, error)
	AssetType() rebuild.AssetType
}
