// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/timing"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDockerBuildPlannerLayers(t *testing.T) {
	planner := NewDockerBuildPlanner()
	opts := build.PlanOptions{
		Resources:     build.Resources{BaseImageConfig: build.BaseImageConfig{Default: "alpine:3.19"}},
		RecordTimings: true,
	}
	for _, tc := range []struct {
		name     string
		strategy rebuild.Strategy
		want     timing.Layers
	}{
		{
			name: "WithDeps",
			strategy: &rebuild.ManualStrategy{
				Location:   rebuild.Location{Repo: "https://github.com/example/test-package", Ref: "v1.0.0"},
				Deps:       "npm install",
				Build:      "npm pack",
				OutputPath: "test-package-1.0.0.tgz",
			},
			want: timing.Layers{Appended: 6, Setup: 0, Source: 1, Deps: 2},
		},
		{
			name: "EmptyDeps",
			strategy: &rebuild.WorkflowStrategy{
				Source:     []flow.Step{{Runs: "echo source"}},
				OutputPath: "test-package-1.0.0.tgz",
			},
			want: timing.Layers{Appended: 5, Setup: 0, Source: 1, Deps: -1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := rebuild.Input{
				Target:   rebuild.Target{Ecosystem: rebuild.NPM, Package: "test-package", Version: "1.0.0", Artifact: "test-package-1.0.0.tgz"},
				Strategy: tc.strategy,
			}
			plan, err := planner.GeneratePlan(context.Background(), input, opts)
			if err != nil {
				t.Fatalf("GeneratePlan failed: %v", err)
			}
			if diff := cmp.Diff(tc.want, plan.Layers); diff != "" {
				t.Errorf("Layers mismatch (-want +got):\n%s", diff)
			}
			// Drift guard: the declared shape matches the rendered Dockerfile.
			if runs := strings.Count(plan.Dockerfile, "\nRUN "); plan.Layers.Appended != runs+2 {
				t.Errorf("Appended = %d, want RUN count + 2 = %d", plan.Layers.Appended, runs+2)
			}
			// Without the option no layers are declared and the executor
			// skips observation.
			noRecord := opts
			noRecord.RecordTimings = false
			plan, err = planner.GeneratePlan(context.Background(), input, noRecord)
			if err != nil {
				t.Fatalf("GeneratePlan failed: %v", err)
			}
			if plan.Layers != (timing.Layers{}) {
				t.Errorf("Layers = %+v, want zero without RecordTimings", plan.Layers)
			}
		})
	}
}
