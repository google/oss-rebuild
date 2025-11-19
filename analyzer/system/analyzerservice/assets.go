// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

// System analyzer specific asset types
const (
	// SystemAnalysisAsset is the signed attestation bundle for system trace analysis.
	SystemAnalysisAsset = rebuild.AssetType("system/rebuild.intoto.jsonl")
	// SystemTraceAsset is the system trace log from rebuild analysis.
	SystemTraceAsset = rebuild.AssetType("system/tetragon.jsonl")
)

// System analyzer specific build type constants
const (
	// BuildTypeSystemTraceRebuildV01 is the build type for system trace rebuild analysis attestations.
	BuildTypeSystemTraceRebuildV01 = "https://docs.oss-rebuild.dev/builds/SystemTraceRebuild@v0.1"
)
