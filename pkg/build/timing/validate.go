// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timing

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

// Validated returns the record when the consistency invariants hold:
// non-negative phases and a measured Build. Violations return nil, so every
// non-nil BuildTimings is invariant-checked.
// TODO: Support partial timing results, revisiting this all-or-nothing
// validation.
func Validated(t rebuild.BuildTimings) *rebuild.BuildTimings {
	if t.Build <= 0 || t.Setup < 0 || t.Source < 0 || t.Deps < 0 {
		return nil
	}
	return &t
}
