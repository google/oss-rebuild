// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/target"
)

// Ecosystem and Target live in the target package, a leaf, so that tools
// which only need to name an artifact do not depend on this package.
type Ecosystem = target.Ecosystem

// Ecosystem constants. These are used to select an ecosystem, and used as prefixes in storage.
const (
	NPM      = target.NPM
	PyPI     = target.PyPI
	CratesIO = target.CratesIO
	Maven    = target.Maven
	Debian   = target.Debian
	RubyGems = target.RubyGems
	OCI      = target.OCI
)

// Target is a single target we might attempt to rebuild.
type Target = target.Target

// Input is a request to rebuild a single target.
type Input struct {
	Target   Target
	Strategy Strategy
}

// BuildPhase names one phase of the standard rebuild plan, in execution
// order. Values match Phase.Name in the executors' plans.
type BuildPhase string

const (
	PhaseSetup  BuildPhase = "setup"
	PhaseSource BuildPhase = "source"
	PhaseDeps   BuildPhase = "deps"
	PhaseBuild  BuildPhase = "build"
)

// BuildTimings describe how long each build phase took. Records may be
// partial: a nil phase carries no data and must never be read as zero. A
// present phase is a measured span. A present zero Deps means the plan had
// no deps phase, stamped only once the build provably progressed past the
// deps slot (a clean record, or one that failed in the build phase), so
// partial records never imply unreached progress.
//
// TODO: Records stored before this shape (the pre-#949 flat Timings and the
// all-or-nothing BuildTimings) are assumed absent: they decode without
// error but carry stray or missing phases. Rederive them from GCB build
// logs if they are ever needed.
type BuildTimings struct {
	Setup  *time.Duration
	Source *time.Duration
	Deps   *time.Duration
	Build  *time.Duration
	// FailedIn names the phase the build failed in. Its span, when present,
	// runs until termination: complete for a nonzero-exit script, a lower
	// bound for a kill or timeout. Exclude it from clean-duration estimates.
	// Phases after it are nil. Phases strictly before it are clean spans.
	// Empty FailedIn does not prove completeness: clocks can be lost on
	// otherwise clean builds.
	FailedIn BuildPhase
}

// Timings aggregate the independently recorded durations of a rebuild.
type Timings struct {
	Infer *time.Duration // nil when inference did not run
	Build *BuildTimings  // nil when no phase was measured, and may be partial otherwise
}

// PrebuildConfig contains deployment-specific prebuild configuration
type PrebuildConfig struct {
	Bucket string `json:"bucket"`
	Dir    string `json:"dir,omitempty"`
	Auth   bool   `json:"auth,omitempty"`
}
