// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Plan represents any execution plan type.
type Plan any

// Planner is a generic interface for generating execution plans from rebuild inputs.
// Each executor type expects a specific Plan type so any conformant planner can be used.
type Planner[T Plan] interface {
	GeneratePlan(ctx context.Context, input rebuild.Input, opts PlanOptions) (T, error)
}
