// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// DockerRunPlan represents a Docker run execution plan where phase scripts
// run sequentially in one container started from an existing image.
type DockerRunPlan struct {
	// Image is the Docker image to run
	Image string
	// Setup installs system deps and fetches tools (e.g. timewarp)
	Setup string
	// Source populates /src with the package source
	Source string
	// Deps installs the package's build deps. Empty when the plan has no
	// deps phase. Timewarp serves only for this phase's duration, matching
	// the docker build variants.
	Deps string
	// Build produces the artifact and copies it to OutputPath
	Build string
	// WorkingDir sets the working directory in the container
	WorkingDir string
	// OutputPath specifies where artifacts should be copied from the container
	OutputPath string
	// RequiresAuth indicates whether the plan requires authentication
	RequiresAuth bool
	// Indicates whether to run the container in privileged mode
	Privileged bool
}

// Phase is one sequentially executed script of a DockerRunPlan. Phase scripts
// are cwd-independent: each phase runs in a fresh shell and no state flows
// between phases beyond the container filesystem.
type Phase struct {
	Name   rebuild.BuildPhase
	Script string
}

// strictPrelude is the shell strict-mode prelude prepended to every rendered
// script.
const strictPrelude = "set -eux"

// Phases returns the plan's scripts in execution order, omitting the deps
// phase when the plan has none.
func (p *DockerRunPlan) Phases() []Phase {
	phases := []Phase{{rebuild.PhaseSetup, p.Setup}, {rebuild.PhaseSource, p.Source}}
	if p.Deps != "" {
		phases = append(phases, Phase{rebuild.PhaseDeps, p.Deps})
	}
	return append(phases, Phase{rebuild.PhaseBuild, p.Build})
}

// CombinedScript renders the phases as one script for display. Phase
// cwd-independence keeps its semantics identical to per-phase execution.
func (p *DockerRunPlan) CombinedScript() string {
	scripts := []string{strictPrelude}
	for _, ph := range p.Phases() {
		scripts = append(scripts, ph.Script)
	}
	return strings.Join(scripts, "\n")
}
