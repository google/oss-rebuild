// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestGemBuildStrategies(t *testing.T) {
	defaultLocation := rebuild.Location{
		Dir:  "the_dir",
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		target   rebuild.Target
		want     rebuild.Instructions
	}{
		{
			name: "basic gem build with ruby version",
			strategy: &GemBuild{
				Location:    defaultLocation,
				RubyVersion: "3.3.6",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   "example",
				Version:   "1.0.0",
				Artifact:  "example-1.0.0.gem",
			},
			want: rebuild.Instructions{
				Location: defaultLocation,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git"},
				},
				Source: "git clone the_repo .\ngit checkout --force 'the_ref'",
				Deps: `apt-get update && apt-get install -y --no-install-recommends build-essential wget ca-certificates libyaml-dev
wget -q -O /tmp/ruby.tar.gz "https://github.com/ruby/ruby-builder/releases/download/ruby-3.3.6/ruby-3.3.6-ubuntu-24.04-x64.tar.gz"
mkdir -p /opt/hostedtoolcache/Ruby/3.3.6/x64
tar xzf /tmp/ruby.tar.gz --strip-components=1 -C /opt/hostedtoolcache/Ruby/3.3.6/x64
ln -sf /opt/hostedtoolcache/Ruby/3.3.6/x64/bin/* /usr/local/bin/
rm -f /tmp/ruby.tar.gz`,
				Build:      "cd the_dir && gem build *.gemspec",
				OutputPath: "the_dir/example-1.0.0.gem",
			},
		},
		{
			name: "gem build no dir",
			strategy: &GemBuild{
				Location: rebuild.Location{
					Dir:  "",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				RubyVersion: "3.4.0",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   "example",
				Version:   "2.0.0",
				Artifact:  "example-2.0.0.gem",
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Dir:  "",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git"},
				},
				Source: "git clone the_repo .\ngit checkout --force 'the_ref'",
				Deps: `apt-get update && apt-get install -y --no-install-recommends build-essential wget ca-certificates libyaml-dev
wget -q -O /tmp/ruby.tar.gz "https://github.com/ruby/ruby-builder/releases/download/ruby-3.4.0/ruby-3.4.0-ubuntu-24.04-x64.tar.gz"
mkdir -p /opt/hostedtoolcache/Ruby/3.4.0/x64
tar xzf /tmp/ruby.tar.gz --strip-components=1 -C /opt/hostedtoolcache/Ruby/3.4.0/x64
ln -sf /opt/hostedtoolcache/Ruby/3.4.0/x64/bin/* /usr/local/bin/
rm -f /tmp/ruby.tar.gz`,
				Build:      "gem build *.gemspec",
				OutputPath: "example-2.0.0.gem",
			},
		},
		{
			name: "gem build no ruby version",
			strategy: &GemBuild{
				Location: defaultLocation,
			},
			target: rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   "example",
				Version:   "1.0.0",
				Artifact:  "example-1.0.0.gem",
			},
			want: rebuild.Instructions{
				Location: defaultLocation,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git"},
				},
				Source:     "git clone the_repo .\ngit checkout --force 'the_ref'",
				Deps:       "",
				Build:      "cd the_dir && gem build *.gemspec",
				OutputPath: "the_dir/example-1.0.0.gem",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			be := rebuild.BuildEnv{HasRepo: false}
			got, err := tc.strategy.GenerateFor(tc.target, be)
			if err != nil {
				t.Fatalf("GenerateFor() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GenerateFor() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}
