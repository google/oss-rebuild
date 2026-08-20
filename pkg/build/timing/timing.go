// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package timing extracts per-phase build timings by observing a completed
// build: docker history layer clocks for the phases and container state
// clocks for the Build phase, never build logs. Records may be partial: a
// nil phase means unmeasured, and Validated is the sole gate deciding
// whether a record carries enough consistent data to emit. Extraction
// failures never affect build outcome.
package timing

// Layers is a planner's declaration of this build's layer positions within
// docker history output.
type Layers struct {
	// Appended counts history entries added above the base image: one per
	// post-FROM instruction, including empty-layer metadata entries.
	Appended int
	// Setup, Source, and Deps index the phase RUN layers among the appended
	// entries, oldest first. Deps is negative when the plan has no deps layer.
	Setup  int
	Source int
	Deps   int
}
