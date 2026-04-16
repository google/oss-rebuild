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
	deps := []flow.Step{}
	if b.RubyVersion != "" {
		deps = append(deps, flow.Step{
			Uses: "rubygems/install-ruby",
			With: map[string]string{
				"rubyVersion": b.RubyVersion,
			},
		})
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: deps,
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
		Name: "rubygems/install-ruby",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- $prefix := printf "/opt/hostedtoolcache/Ruby/%s/x64" .With.rubyVersion -}}
				apt-get update && apt-get install -y --no-install-recommends build-essential wget ca-certificates libyaml-dev
				wget -q -O /tmp/ruby.tar.gz "https://github.com/ruby/ruby-builder/releases/download/ruby-{{.With.rubyVersion}}/ruby-{{.With.rubyVersion}}-ubuntu-24.04-x64.tar.gz"
				mkdir -p {{$prefix}}
				tar xzf /tmp/ruby.tar.gz --strip-components=1 -C {{$prefix}}
				ln -sf {{$prefix}}/bin/* /usr/local/bin/
				rm -f /tmp/ruby.tar.gz`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "rubygems/build/gem",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- $dir := .With.dir -}}
				{{- if and (ne $dir ".") (ne $dir "")}}cd {{$dir}} && {{end -}}
				gem build *.gemspec`)[1:],
			Needs: []string{},
		}},
	},
}
