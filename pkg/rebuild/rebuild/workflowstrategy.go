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
	"bytes"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/pkg/errors"
)

func chain[T any](slices ...[]T) []T {
	elems := make([]T, 0)
	for _, slice := range slices {
		elems = append(elems, slice...)
	}
	return elems
}

// WorkflowStep represents a single step in the workflow build process.
type WorkflowStep struct {
	Runs string            `json:"runs" yaml:"runs,omitempty"`
	Uses string            `json:"uses" yaml:"uses,omitempty"`
	With map[string]string `json:"with" yaml:"with,omitempty"`
}

// WorkflowStrategy allows use of composable steps to define the build.
type WorkflowStrategy struct {
	Location
	Source     []WorkflowStep `json:"src" yaml:"src,omitempty"`
	Deps       []WorkflowStep `json:"deps" yaml:"deps,omitempty"`
	Build      []WorkflowStep `json:"build" yaml:"build,omitempty"`
	SystemDeps []string       `json:"system_deps" yaml:"system_deps,omitempty"`
	OutputPath string         `json:"output_path" yaml:"output_path,omitempty"`
}

var _ Strategy = &WorkflowStrategy{}

// GenerateFor generates the instructions for a MuddleStrategy.
func (s *WorkflowStrategy) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	source, err := s.generateForSteps(s.Source, t, be)
	if err != nil {
		return Instructions{}, errors.Wrap(err, "generating source steps")
	}
	deps, err := s.generateForSteps(s.Deps, t, be)
	if err != nil {
		return Instructions{}, errors.Wrap(err, "generating dependency steps")
	}
	build, err := s.generateForSteps(s.Build, t, be)
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
		OutputPath: s.OutputPath,
	}, nil
}

// task defines a task with its system requirements.
type task struct {
	Script string
	Needs  []string // Required system deps
}

func (tsk task) Join(other task) task {
	return task{
		Script: strings.Join([]string{tsk.Script, other.Script}, "\n"),
		Needs:  append(tsk.Needs, other.Needs...),
	}
}

// generateForSteps processes a slice of MuddleSteps and returns a combined script
func (s *WorkflowStrategy) generateForSteps(steps []WorkflowStep, t Target, be BuildEnv) (task, error) {
	var ret task
	for i, step := range steps {
		cmd, err := s.generateForStep(step, t, be)
		if err != nil {
			return task{}, err
		}
		if i == 0 {
			ret = cmd
		} else {
			ret = ret.Join(cmd)
		}
	}
	return ret, nil
}

// generateForStep generates the shell script for a single MuddleStep
func (s *WorkflowStrategy) generateForStep(step WorkflowStep, t Target, be BuildEnv) (task, error) {
	if (step.Runs == "") == (step.Uses == "") {
		return task{}, errors.New("exactly one of 'runs' or 'uses' must be provided")
	}
	if step.Runs != "" {
		return task{Script: step.Runs}, nil
	}
	tool, ok := toolkit[step.Uses]
	if !ok {
		return task{}, errors.Errorf("unknown 'uses' tool: %s", step.Uses)
	}
	buf := &bytes.Buffer{}
	data := struct {
		With     map[string]string
		Target   Target
		BuildEnv BuildEnv
		Location Location
	}{
		With:     step.With,
		Target:   t,
		BuildEnv: be,
		Location: s.Location,
	}
	err := tool.Template.Execute(buf, data)
	if err != nil {
		return task{}, errors.Wrap(err, "executing template")
	}
	return task{Script: buf.String(), Needs: tool.Needs}, nil
}

// tool defines a task template with its system requirements.
type tool struct {
	Template *template.Template
	Needs    []string // Required system deps
}

var toolkit = map[string]*tool{
	"git-checkout": {
		Template: template.Must(template.New("git-checkout").Parse(textwrap.Dedent(`
				{{ if not .BuildEnv.HasRepo -}}
				git clone {{.Location.Repo}} .
				{{ end -}}
				git checkout --force '{{.Location.Ref}}'`)[1:],
		)),
		Needs: []string{"git"},
	},
	"npm/install": {
		Template: template.Must(template.New("npm/install").Parse(textwrap.Dedent(`
				PATH=/usr/local/bin:/usr/bin npx --package=npm{{if ne .With.npmVersion ""}}@{{.With.npmVersion}}{{end}} -c '
						{{- if and (ne .Location.Dir ".") (ne .Location.Dir "")}}cd {{.Location.Dir}} && {{end -}}
						npm install --force'`)[1:],
		)).Option("missingkey=zero"),
		Needs: []string{"npm"},
	},
}

type Flowable interface {
	ToWorkflow() *WorkflowStrategy
}
