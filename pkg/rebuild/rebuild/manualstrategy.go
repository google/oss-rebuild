// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import "github.com/google/oss-rebuild/pkg/rebuild/flow"

// ManualStrategy allows full control over the build instruction steps, for builds that don't fit any other strategy.
type ManualStrategy struct {
	Location
	Deps       string      `json:"deps" yaml:"deps,omitempty"`
	Build      string      `json:"build" yaml:"build,omitempty"`
	Requires   RequiredEnv `json:"requires" yaml:"requires,omitempty"`
	OutputPath string      `json:"output_path" yaml:"output_path,omitempty"`
}

var _ Strategy = &ManualStrategy{}

func (s *ManualStrategy) ToWorkflow() *WorkflowStrategy {
	return &WorkflowStrategy{
		Location:   s.Location,
		Source:     []flow.Step{{Uses: "git-checkout"}},
		Deps:       []flow.Step{{Runs: s.Deps}},
		Build:      []flow.Step{{Runs: s.Build}},
		Requires:   s.Requires,
		OutputPath: s.OutputPath,
	}
}

// GenerateFor generates the instructions for a ManualStrategy.
func (s *ManualStrategy) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	return s.ToWorkflow().GenerateFor(t, be)
}
