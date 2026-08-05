// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
)

// Ecosystem represents a package ecosystem.
type Ecosystem string

// Ecosystem constants. These are used to select an ecosystem, and used as prefixes in storage.
const (
	NPM      Ecosystem = "npm"
	PyPI     Ecosystem = "pypi"
	CratesIO Ecosystem = "cratesio"
	Maven    Ecosystem = "maven"
	Debian   Ecosystem = "debian"
	RubyGems Ecosystem = "rubygems"
	OCI      Ecosystem = "oci"
)

// Target is a single target we might attempt to rebuild.
type Target struct {
	Ecosystem Ecosystem
	Package   string
	Version   string
	Artifact  string
}

// ArchiveType provide the Target's archive.Format.
func (t Target) ArchiveType() archive.Format {
	switch t.Ecosystem {
	case Debian:
		return archive.RawFormat
	case CratesIO, NPM:
		return archive.TarGzFormat
	case PyPI:
		switch {
		case strings.HasSuffix(t.Artifact, ".whl"), strings.HasSuffix(t.Artifact, ".zip"):
			return archive.ZipFormat
		case strings.HasSuffix(t.Artifact, ".tar.gz"):
			return archive.TarGzFormat
		// Deprecated in https://peps.python.org/pep-0715/
		case strings.HasSuffix(t.Artifact, ".egg"):
			return archive.ZipFormat
		// Deprecated in https://peps.python.org/pep-0527/
		case strings.HasSuffix(t.Artifact, ".tgz"), strings.HasSuffix(t.Artifact, ".tar.Z"):
			return archive.TarGzFormat
		case strings.HasSuffix(t.Artifact, ".tar"):
			return archive.TarFormat
		case strings.HasSuffix(t.Artifact, ".tar.bz2"), strings.HasSuffix(t.Artifact, ".tbz"):
			return archive.UnknownFormat // bzip2
		case strings.HasSuffix(t.Artifact, ".tar.xz"):
			return archive.UnknownFormat // xz
		default:
			return archive.UnknownFormat
		}
	case Maven:
		if strings.HasSuffix(t.Artifact, ".jar") {
			return archive.ZipFormat
		} else if strings.HasSuffix(t.Artifact, ".pom") {
			return archive.RawFormat
		}
		return archive.UnknownFormat
	case RubyGems:
		// Gem files are tar archives containing data.tar.gz, metadata.gz, and checksums.yaml.gz
		return archive.TarFormat
	case OCI:
		return archive.TarFormat
	default:
		return archive.UnknownFormat
	}
}

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
