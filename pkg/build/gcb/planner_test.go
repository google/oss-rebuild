// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestMakeDockerfile(t *testing.T) {
	type testCase struct {
		name        string
		input       rebuild.Input
		opts        build.PlanOptions
		expected    string
		expectedErr bool
	}

	// Create a basic base image config for testing
	baseImageConfig := build.BaseImageConfig{
		Default: "docker.io/library/alpine:3.19",
	}

	testCases := []testCase{
		{
			name: "Basic Usage",
			input: rebuild.Input{
				Target: rebuild.Target{},
				Strategy: &rebuild.ManualStrategy{
					Location: rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					Requires: rebuild.RequiredEnv{
						SystemDeps: []string{"git", "make"},
					},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: build.PlanOptions{
				UseTimewarp:     false,
				UseNetworkProxy: false,
				Resources: build.Resources{
					BaseImageConfig: baseImageConfig,
				},
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<-'EOF'
	set -eux
	apk add git make
	EOF
RUN <<-'EOF'
	set -eux
	mkdir /src && cd /src
	git clone github.com/example .
	git checkout --force 'main'
	make deps ...
	EOF
COPY --chmod=755 <<-'EOF' /build
	set -eux
	make build ...
	mkdir /out && cp /src/output/foo.tgz /out/
	EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "With Timewarp",
			input: rebuild.Input{
				Target: rebuild.Target{},
				Strategy: &rebuild.ManualStrategy{
					Location: rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					Requires: rebuild.RequiredEnv{
						SystemDeps: []string{"git", "make"},
					},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: build.PlanOptions{
				UseTimewarp:     true,
				UseNetworkProxy: false,
				Resources: build.Resources{
					BaseImageConfig: baseImageConfig,
					ToolURLs: map[build.ToolType]string{
						build.TimewarpTool: "https://my-bucket.storage.googleapis.com/timewarp",
					},
				},
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN <<-'EOF'
	set -eux
	wget https://my-bucket.storage.googleapis.com/timewarp
	chmod +x timewarp
	apk add git make
	EOF
RUN <<-'EOF'
	set -eux
	./timewarp -port 8080 &
	while ! nc -z localhost 8080;do sleep 1;done
	mkdir /src && cd /src
	git clone github.com/example .
	git checkout --force 'main'
	make deps ...
	EOF
COPY --chmod=755 <<-'EOF' /build
	set -eux
	make build ...
	mkdir /out && cp /src/output/foo.tgz /out/
	EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
		{
			name: "With Timewarp and auth",
			input: rebuild.Input{
				Target: rebuild.Target{},
				Strategy: &rebuild.ManualStrategy{
					Location: rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
					Requires: rebuild.RequiredEnv{
						SystemDeps: []string{"git", "make"},
					},
					Deps:       "make deps ...",
					Build:      "make build ...",
					OutputPath: "output/foo.tgz",
				},
			},
			opts: build.PlanOptions{
				UseTimewarp:     true,
				UseNetworkProxy: false,
				Resources: build.Resources{
					BaseImageConfig: baseImageConfig,
					ToolURLs: map[build.ToolType]string{
						build.TimewarpTool: "https://my-bucket.storage.googleapis.com/timewarp",
					},
					ToolAuthRequired: []string{"https://my-bucket.storage.googleapis.com/"},
				},
			},
			expected: `#syntax=docker/dockerfile:1.10
FROM docker.io/library/alpine:3.19
RUN --mount=type=secret,id=auth_header <<-'EOF'
	set -eux
	apk add curl && curl -O -H @/run/secrets/auth_header https://my-bucket.storage.googleapis.com/timewarp
	chmod +x timewarp
	apk add git make
	EOF
RUN <<-'EOF'
	set -eux
	./timewarp -port 8080 &
	while ! nc -z localhost 8080;do sleep 1;done
	mkdir /src && cd /src
	git clone github.com/example .
	git checkout --force 'main'
	make deps ...
	EOF
COPY --chmod=755 <<-'EOF' /build
	set -eux
	make build ...
	mkdir /out && cp /src/output/foo.tgz /out/
	EOF
WORKDIR "/src"
ENTRYPOINT ["/bin/sh","/build"]
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := PlannerConfig{
				Project:        "test-project",
				ServiceAccount: "test@test.iam.gserviceaccount.com",
			}
			instructions, _ := tc.input.Strategy.GenerateFor(tc.input.Target, rebuild.BuildEnv{TimewarpHost: "localhost:8080"})
			planner := NewPlanner(config)
			result, err := planner.generateDockerfile(instructions, tc.input, tc.opts)

			if tc.expectedErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("Dockerfile mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGCBPlannerGeneratePlan(t *testing.T) {
	ctx := context.Background()

	config := PlannerConfig{
		Project:        "test-project",
		ServiceAccount: "test@test.iam.gserviceaccount.com",
	}
	planner := NewPlanner(config)

	baseImageConfig := build.BaseImageConfig{
		Default: "docker.io/library/alpine:3.19",
	}

	input := rebuild.Input{
		Target: rebuild.Target{
			Ecosystem: rebuild.NPM,
			Package:   "test-package",
			Version:   "1.0.0",
			Artifact:  "test-package-1.0.0.tgz",
		},
		Strategy: &rebuild.ManualStrategy{
			Location: rebuild.Location{Repo: "github.com/example", Ref: "main", Dir: "/src"},
			Requires: rebuild.RequiredEnv{
				SystemDeps: []string{"git", "node", "npm"},
			},
			Deps:       "npm install",
			Build:      "npm run build",
			OutputPath: "dist/test-package-1.0.0.tgz",
		},
	}

	opts := build.PlanOptions{
		UseTimewarp:     false,
		UseNetworkProxy: false,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
		},
	}

	plan, err := planner.GeneratePlan(ctx, input, opts)
	if err != nil {
		t.Fatalf("GeneratePlan failed: %v", err)
	}

	if plan == nil {
		t.Fatal("Plan should not be nil")
	}

	if plan.Dockerfile == "" {
		t.Error("Dockerfile should not be empty")
	}

	if len(plan.Steps) == 0 {
		t.Error("Steps should not be empty")
	}

	// Check that the plan contains expected Cloud Build steps
	foundBuildStep := false
	foundExtractStep := false
	foundSaveStep := false
	foundUploadStep := false

	for _, step := range plan.Steps {
		if step.Name == "gcr.io/cloud-builders/docker" {
			if step.Script != "" && !foundBuildStep {
				foundBuildStep = true
			} else if len(step.Args) > 0 && step.Args[0] == "cp" {
				foundExtractStep = true
			} else if step.Script != "" && strings.Contains(step.Script, "docker save") {
				foundSaveStep = true
			}
		} else if step.Name == baseImageConfig.Default {
			foundUploadStep = true
		}
	}

	if !foundBuildStep {
		t.Error("Expected build step not found")
	}
	if !foundExtractStep {
		t.Error("Expected extract step not found")
	}
	if !foundSaveStep {
		t.Error("Expected save step not found")
	}
	if !foundUploadStep {
		t.Error("Expected upload step not found")
	}
}
