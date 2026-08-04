// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timing

import (
	"slices"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// phaseOrder is the standard plan's execution order.
var phaseOrder = []rebuild.BuildPhase{rebuild.PhaseSetup, rebuild.PhaseSource, rebuild.PhaseDeps, rebuild.PhaseBuild}

// Validated returns the record when the partial-timing invariants hold: at
// least one phase measured, every measured phase non-negative and Build,
// when measured, positive, and FailedIn either empty or a known phase with
// every later phase unmeasured. Violations return nil, so every non-nil
// BuildTimings is invariant-checked. The failing phase itself may be
// unmeasured: failure clocks are not always observable.
func Validated(t rebuild.BuildTimings) *rebuild.BuildTimings {
	spans := []*time.Duration{t.Setup, t.Source, t.Deps, t.Build}
	failedAt := len(spans)
	if t.FailedIn != "" {
		if failedAt = slices.Index(phaseOrder, t.FailedIn); failedAt < 0 {
			return nil
		}
	}
	var measured bool
	for i, d := range spans {
		switch {
		case d == nil:
		case *d < 0, *d == 0 && phaseOrder[i] == rebuild.PhaseBuild, i > failedAt:
			return nil
		default:
			measured = true
		}
	}
	if !measured {
		return nil
	}
	return &t
}
