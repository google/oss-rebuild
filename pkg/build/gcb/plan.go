// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"slices"

	"github.com/google/oss-rebuild/pkg/build/timing"
	"google.golang.org/api/cloudbuild/v1"
)

// timingStepID identifies the appended timing observation step, found by Id
// since step numbering shifts with the conditional save and export steps.
const timingStepID = "timing"

// Plan represents a Google Cloud Build execution plan
type Plan struct {
	// Steps contains the Cloud Build steps to execute
	Steps []*cloudbuild.BuildStep
	// Dockerfile contains the generated Dockerfile content
	Dockerfile string
	// Layers declare the plan's layer positions for timing extraction
	Layers timing.Layers
}

// timingStep returns the index of the appended timing step, negative when absent.
func (p *Plan) timingStep() int {
	return slices.IndexFunc(p.Steps, func(s *cloudbuild.BuildStep) bool { return s.Id == timingStepID })
}
