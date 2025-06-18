// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

// Network analyzer specific asset types
const (
	// NetworkAnalysisAsset is the signed attestation bundle for network analysis.
	NetworkAnalysisAsset = rebuild.AssetType("network/rebuild.intoto.jsonl")
	// NetworkLogAsset is the network activity log from rebuild analysis.
	NetworkLogAsset = rebuild.AssetType("network/netlog.json")
)

// Network analyzer specific build type constants
const (
	// BuildTypeNetworkRebuildV01 is the build type for network rebuild analysis attestations.
	BuildTypeNetworkRebuildV01 = "https://docs.oss-rebuild.dev/builds/NetworkRebuild@v0.1"
)
