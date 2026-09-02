// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import (
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// EcosystemSystem maps a local Ecosystem to its deps.dev `System`.
// This allows querying against the enum in bigquery-public-data.deps_dev_v1.
var EcosystemSystem = map[rebuild.Ecosystem]string{
	rebuild.NPM:      "NPM",
	rebuild.PyPI:     "PYPI",
	rebuild.CratesIO: "CARGO",
	rebuild.RubyGems: "RUBYGEMS",
	rebuild.Maven:    "MAVEN",
}

// PrevalenceRecord contains the number of distinct packages that depend on a
// package, directly or transitively, resolved against the latest deps.dev
// dependency-graph snapshot, and that count scored against its ecosystem.
// Version is omitted for package-level rows and set for version-level ones.
//
// Version granularity is pertinent because the most-depended-upon version is
// rarely the newest and old versions may retain use (and, thus, value) well
// past their publication. Ranking a package's versions by its
// package-level prevalence would leave recency as the only differentiator,
// which may not correlate strongly with blast radius.
type PrevalenceRecord struct {
	Ecosystem  string `json:"ecosystem"`
	Package    string `json:"package"`
	Version    string `json:"version,omitempty"`
	Dependents int64  `json:"dependents"`
	// Prevalence is Dependents on a log scale relative to the ecosystem's most
	// depended-on package, in (0,1]. Counts are tail heavy enough that a rank
	// quantile over the ecosystem would crowd every exported row above 0.99.
	Prevalence float64 `json:"prevalence"`
	// Artifact and Published carry the version's registry metadata so a
	// consumer can form a rebuild target and rank by age without a live
	// registry call. Both are zero on package-level rows. Artifact is set
	// only for PyPI, where the name is not derivable from the version, and
	// only when a pure wheel exists. Published is zero when no source
	// records a time.
	Artifact  string    `json:"artifact,omitempty"`
	Published time.Time `json:"published,omitzero"`
}
