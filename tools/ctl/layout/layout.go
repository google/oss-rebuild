// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package layout

const (
	AssetsDir          = "assets"     // General AssetStore for logs, artifacts, diffs, etc.
	BuildDefsDir       = "build-defs" // Storage of build definitions.
	RundexDir          = "rundex"     // The metadata about runs and rebuild attempts.
	RundexRunsDir      = "runs_metadata"
	RundexRunsPath     = RundexDir + "/" + RundexRunsDir // Metadata about runs.
	RundexRebuildsDir  = "runs"
	RundexRebuildsPath = RundexDir + "/" + RundexRebuildsDir // Rebuild records.
)
