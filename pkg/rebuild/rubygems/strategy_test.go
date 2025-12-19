// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestGemBuild_GenerateFor(t *testing.T) {
	tests := []struct {
		name     string
		build    GemBuild
		target   rebuild.Target
		wantDeps []string
	}{
		{
			name: "basic gem build",
			build: GemBuild{
				Location: rebuild.Location{
					Repo: "https://github.com/rails/rails",
					Ref:  "v7.1.0",
					Dir:  "",
				},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   "rails",
				Version:   "7.1.0",
				Artifact:  "rails-7.1.0.gem",
			},
			wantDeps: []string{"git", "ruby"},
		},
		{
			name: "gem build with subdir",
			build: GemBuild{
				Location: rebuild.Location{
					Repo: "https://github.com/rails/rails",
					Ref:  "v7.1.0",
					Dir:  "activerecord",
				},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   "activerecord",
				Version:   "7.1.0",
				Artifact:  "activerecord-7.1.0.gem",
			},
			wantDeps: []string{"git", "ruby"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := rebuild.BuildEnv{HasRepo: false}
			instructions, err := tt.build.GenerateFor(tt.target, be)
			if err != nil {
				t.Fatalf("GenerateFor() error = %v", err)
			}

			// Check that ruby is in the system deps
			hasRuby := false
			for _, dep := range instructions.Requires.SystemDeps {
				if dep == "ruby" {
					hasRuby = true
					break
				}
			}
			if !hasRuby {
				t.Errorf("SystemDeps = %v, want to contain 'ruby'", instructions.Requires.SystemDeps)
			}

			// Check that the build script contains gem build
			if instructions.Build == "" {
				t.Error("Build script is empty")
			}
		})
	}
}

func TestGemBuild_ToWorkflow(t *testing.T) {
	build := GemBuild{
		Location: rebuild.Location{
			Repo: "https://github.com/example/gem",
			Ref:  "v1.0.0",
			Dir:  "lib",
		},
	}

	workflow := build.ToWorkflow()

	if workflow.Location.Repo != build.Location.Repo {
		t.Errorf("workflow.Location.Repo = %q, want %q", workflow.Location.Repo, build.Location.Repo)
	}
	if workflow.Location.Ref != build.Location.Ref {
		t.Errorf("workflow.Location.Ref = %q, want %q", workflow.Location.Ref, build.Location.Ref)
	}
	if len(workflow.Source) != 1 {
		t.Errorf("len(workflow.Source) = %d, want 1", len(workflow.Source))
	}
	if len(workflow.Build) != 1 {
		t.Errorf("len(workflow.Build) = %d, want 1", len(workflow.Build))
	}
	if workflow.Build[0].Uses != "rubygems/build/gem" {
		t.Errorf("workflow.Build[0].Uses = %q, want %q", workflow.Build[0].Uses, "rubygems/build/gem")
	}
}
