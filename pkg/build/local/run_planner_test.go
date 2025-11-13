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
				PreferPreciseToolchain: true,
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
				Script: textwrap.Dedent(`
			set -eux
			apk update
			mkdir /src && cd /src
			apk add npm git
			git clone https://github.com/example/test-package .
			git checkout --force 'v1.0.0'
			npm install
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
				UseTimewarp:            true,
				PreferPreciseToolchain: true,
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
				Script: textwrap.Dedent(`
			set -eux
			apk update
			apk add curl
			curl  https://example.com/timewarp > timewarp
			chmod +x timewarp
			./timewarp -port 8081 &
			while ! nc -z localhost 8081;do sleep 1;done
			mkdir /src && cd /src
			apk add git
			git clone https://github.com/example/test-package .
			git checkout --force 'v1.0.0'
			npm install
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
