// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rebuild

import "github.com/google/oss-rebuild/pkg/rebuild/flow"

// ManualStrategy allows full control over the build instruction steps, for builds that don't fit any other strategy.
type ManualStrategy struct {
	Location
	Deps       string   `json:"deps" yaml:"deps,omitempty"`
	Build      string   `json:"build" yaml:"build,omitempty"`
	SystemDeps []string `json:"system_deps" yaml:"system_deps,omitempty"`
	OutputPath string   `json:"output_path" yaml:"output_path,omitempty"`
}

var _ Strategy = &ManualStrategy{}

func (s *ManualStrategy) ToWorkflow() *WorkflowStrategy {
	return &WorkflowStrategy{
		Location:   s.Location,
		Source:     []flow.Step{{Uses: "git-checkout"}},
		Deps:       []flow.Step{{Runs: s.Deps}},
		Build:      []flow.Step{{Runs: s.Build}},
		SystemDeps: s.SystemDeps,
		OutputPath: s.OutputPath,
	}
}

// GenerateFor generates the instructions for a ManualStrategy.
func (s *ManualStrategy) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	return s.ToWorkflow().GenerateFor(t, be)
}
