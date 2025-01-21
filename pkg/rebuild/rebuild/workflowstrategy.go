// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package rebuild

import (
	"path"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/pkg/errors"
)

func chain[T any](slices ...[]T) []T {
	elems := make([]T, 0)
	for _, slice := range slices {
		elems = append(elems, slice...)
	}
	return elems
}

// WorkflowStrategy allows use of composable steps to define the build.
type WorkflowStrategy struct {
	Location
	Source     []flow.Step `json:"src" yaml:"src,omitempty"`
	Deps       []flow.Step `json:"deps" yaml:"deps,omitempty"`
	Build      []flow.Step `json:"build" yaml:"build,omitempty"`
	SystemDeps []string    `json:"system_deps" yaml:"system_deps,omitempty"`
	OutputPath string      `json:"output_path" yaml:"output_path,omitempty"`
	OutputDir  string      `json:"output_dir" yaml:"output_dir,omitempty"`
}

var _ Strategy = &WorkflowStrategy{}

// GenerateFor generates the instructions for a WorkflowStrategy.
func (s *WorkflowStrategy) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	var outputPath string
	if (s.OutputDir != "") && (s.OutputPath != "") {
		return Instructions{}, errors.New("only one of OutputPath and OutputDir may be provided")
	} else if s.OutputPath != "" {
		outputPath = s.OutputPath
	} else if s.OutputDir != "" {
		outputPath = path.Join(s.OutputDir, t.Artifact)
	} else {
		// NOTE: This is potentially unexpected default behavior.
		outputPath = t.Artifact
	}
	data := map[string]any{
		"Location": &s.Location,
		"BuildEnv": &be,
		"Target":   &t,
	}
	source, err := flow.ResolveSteps(s.Source, nil, data)
	if err != nil {
		return Instructions{}, errors.Wrap(err, "generating source steps")
	}
	deps, err := flow.ResolveSteps(s.Deps, nil, data)
	if err != nil {
		return Instructions{}, errors.Wrap(err, "generating dependency steps")
	}
	build, err := flow.ResolveSteps(s.Build, nil, data)
	if err != nil {
		return Instructions{}, errors.Wrap(err, "generating build steps")
	}
	uniqueDeps := make(map[string]bool)
	var finalDeps []string
	for _, dep := range chain(s.SystemDeps, source.Needs, deps.Needs, build.Needs) {
		if _, ok := uniqueDeps[dep]; !ok {
			finalDeps = append(finalDeps, dep)
			uniqueDeps[dep] = true
		}
	}
	return Instructions{
		Location:   s.Location,
		Source:     source.Script,
		Deps:       deps.Script,
		Build:      build.Script,
		SystemDeps: finalDeps,
		OutputPath: outputPath,
	}, nil
}

func init() {
	flow.Tools.MustRegister(&flow.Tool{
		Name: "git-checkout",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{ if not .BuildEnv.HasRepo -}}
				git clone {{.Location.Repo}} .
				{{ end -}}
				git checkout --force '{{.Location.Ref}}'`)[1:],
			Needs: []string{"git"},
		}},
	})
	flow.Tools.MustRegister(&flow.Tool{
		Name: "npm/install",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				PATH=/usr/local/bin:/usr/bin npx --package=npm{{if ne .With.npmVersion ""}}@{{.With.npmVersion}}{{end}} -c '
						{{- if and (ne .Location.Dir ".") (ne .Location.Dir "")}}cd {{.Location.Dir}} && {{end -}}
						npm install --force'`)[1:],
			Needs: []string{"npm"},
		}},
	})
}

type Flowable interface {
	ToWorkflow() *WorkflowStrategy
}
