// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDockerRunPlanner(t *testing.T) {
	testCases := []struct {
		name        string
		input       rebuild.Input
		opts        build.PlanOptions
		expected    *DockerRunPlan
		expectedErr string
	}{
		{
			name: "default",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-package",
					Version:   "1.0.0",
					Artifact:  "test-package-1.0.0.tgz",
				},
				Strategy: &rebuild.ManualStrategy{
					Location: rebuild.Location{
						Repo: "https://github.com/example/test-package",
						Ref:  "v1.0.0",
					},
					Requires: rebuild.RequiredEnv{
						SystemDeps: []string{"npm", "git"},
					},
					Deps:       "npm install",
					Build:      "npm pack",
					OutputPath: "test-package-1.0.0.tgz",
				},
			},
			opts: build.PlanOptions{
				Resources: build.Resources{
					BaseImageConfig: build.BaseImageConfig{
						Default: "alpine:3.19",
					},
				},
			},
			expected: &DockerRunPlan{
				Image:      "alpine:3.19",
				WorkingDir: "/workspace",
				OutputPath: "/out/rebuild",
				Setup: textwrap.Dedent(`
			apk update
			apk add npm git`[1:]),
				Source: textwrap.Dedent(`
			mkdir -p /src && cd /src
			git clone https://github.com/example/test-package .
			git checkout --force 'v1.0.0'`[1:]),
				Deps: textwrap.Dedent(`
			cd /src
			npm install`[1:]),
				Build: textwrap.Dedent(`
			cd /src
			npm pack
			cp /src/test-package-1.0.0.tgz /out/rebuild`[1:]),
			},
		},
		{
			name: "with timewarp",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-package",
					Version:   "1.0.0",
					Artifact:  "test-package-1.0.0.tgz",
				},
				Strategy: &rebuild.ManualStrategy{
					Location: rebuild.Location{
						Repo: "https://github.com/example/test-package",
						Ref:  "v1.0.0",
					},
					Deps:       "npm install",
					Build:      "npm pack",
					OutputPath: "test-package-1.0.0.tgz",
				},
			},
			opts: build.PlanOptions{
				UseTimewarp: true,
				Resources: build.Resources{
					BaseImageConfig: build.BaseImageConfig{
						Default: "alpine:3.19",
					},
					ToolURLs: map[build.ToolType]string{
						build.TimewarpTool: "https://example.com/timewarp",
					},
				},
			},
			expected: &DockerRunPlan{
				Image:      "alpine:3.19",
				WorkingDir: "/workspace",
				OutputPath: "/out/rebuild",
				Setup: textwrap.Dedent(`
			apk add curl
			curl https://example.com/timewarp > /timewarp
			chmod +x /timewarp
			apk update
			apk add git`[1:]),
				Source: textwrap.Dedent(`
			mkdir -p /src && cd /src
			git clone https://github.com/example/test-package .
			git checkout --force 'v1.0.0'`[1:]),
				Deps: textwrap.Dedent(`
			/timewarp -port 8081 &
			while ! nc -z localhost 8081;do sleep 1;done
			cd /src
			npm install`[1:]),
				Build: textwrap.Dedent(`
			cd /src
			npm pack
			cp /src/test-package-1.0.0.tgz /out/rebuild`[1:]),
			},
		},
		{
			// No deps instructions: no deps phase, and timewarp is fetched
			// but never started, matching the docker build variants.
			name: "timewarp without deps",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-package",
					Version:   "1.0.0",
					Artifact:  "test-package-1.0.0.tgz",
				},
				Strategy: &rebuild.WorkflowStrategy{
					Source:     []flow.Step{{Runs: "echo source"}},
					Build:      []flow.Step{{Runs: "npm pack"}},
					OutputPath: "test-package-1.0.0.tgz",
				},
			},
			opts: build.PlanOptions{
				UseTimewarp: true,
				Resources: build.Resources{
					BaseImageConfig: build.BaseImageConfig{
						Default: "alpine:3.19",
					},
					ToolURLs: map[build.ToolType]string{
						build.TimewarpTool: "https://example.com/timewarp",
					},
				},
			},
			expected: &DockerRunPlan{
				Image:      "alpine:3.19",
				WorkingDir: "/workspace",
				OutputPath: "/out/rebuild",
				Setup: textwrap.Dedent(`
			apk add curl
			curl https://example.com/timewarp > /timewarp
			chmod +x /timewarp
			apk update
			apk add`[1:]),
				Source: textwrap.Dedent(`
			mkdir -p /src && cd /src
			echo source`[1:]),
				Build: textwrap.Dedent(`
			cd /src
			npm pack
			cp /src/test-package-1.0.0.tgz /out/rebuild`[1:]),
			},
		},
		{
			name: "error handling",
			input: rebuild.Input{
				Target: rebuild.Target{
					Ecosystem: rebuild.NPM,
					Package:   "test-package",
					Version:   "1.0.0",
					Artifact:  "test-package-1.0.0.tgz",
				},
				Strategy: &rebuild.LocationHint{
					Location: rebuild.Location{
						Repo: "https://github.com/example/test-package",
						Ref:  "v1.0.0",
					},
				},
			},
			opts: build.PlanOptions{
				Resources: build.Resources{
					BaseImageConfig: build.BaseImageConfig{
						Default: "ubuntu:22.04",
					},
				},
			},
			expectedErr: "failed to generate rebuild instructions",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			planner := NewDockerRunPlanner()
			plan, err := planner.GeneratePlan(context.Background(), tc.input, tc.opts)
			if tc.expectedErr != "" {
				if err == nil {
					t.Fatalf("GeneratePlan() expected error, but got none")
				}
				if !strings.Contains(err.Error(), tc.expectedErr) {
					t.Errorf("GeneratePlan() error = %v, wantErr %v", err, tc.expectedErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GeneratePlan() failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, plan); diff != "" {
				t.Errorf("GeneratePlan() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDockerRunPlanPhases(t *testing.T) {
	plan := &DockerRunPlan{Setup: "s1", Source: "s2", Deps: "s3", Build: "s4"}
	want := []Phase{{"setup", "s1"}, {"source", "s2"}, {"deps", "s3"}, {"build", "s4"}}
	if diff := cmp.Diff(want, plan.Phases()); diff != "" {
		t.Errorf("Phases() mismatch (-want +got):\n%s", diff)
	}
	plan.Deps = ""
	want = []Phase{{"setup", "s1"}, {"source", "s2"}, {"build", "s4"}}
	if diff := cmp.Diff(want, plan.Phases()); diff != "" {
		t.Errorf("Phases() without deps mismatch (-want +got):\n%s", diff)
	}
	if got, want := plan.CombinedScript(), "set -eux\ns1\ns2\ns4"; got != want {
		t.Errorf("CombinedScript() = %q, want %q", got, want)
	}
}
