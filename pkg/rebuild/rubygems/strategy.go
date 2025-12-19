// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// GemBuild aggregates the options controlling a gem build.
type GemBuild struct {
	rebuild.Location
	// RubyVersion is the version of Ruby to use for the build.
	RubyVersion string `json:"ruby_version,omitempty" yaml:"ruby_version,omitempty"`
}

var _ rebuild.Strategy = &GemBuild{}

func (b *GemBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps:  []flow.Step{},
		Build: []flow.Step{{
			Uses: "rubygems/build/gem",
			With: map[string]string{
				"dir": b.Location.Dir,
			},
		}},
		OutputDir: b.Location.Dir,
	}
}

// GenerateFor generates the instructions for a GemBuild.
func (b *GemBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

// Base tools for individual operations
var toolkit = []*flow.Tool{
	{
		Name: "rubygems/build/gem",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- $dir := .With.dir -}}
				{{- if and (ne $dir ".") (ne $dir "")}}cd {{$dir}} && {{end -}}
				gem build *.gemspec`)[1:],
			Needs: []string{"ruby"},
		}},
	},
}
