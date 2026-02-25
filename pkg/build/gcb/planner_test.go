// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"google.golang.org/api/cloudbuild/v1"
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
RUN sed 's/^ //' <<'EOF' | sh
 set -eux
 apk add git make
EOF
RUN sed 's/^ //' <<'EOF' | sh
 set -eux
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN sed 's/^ //' <<'EOF' >/build
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
RUN sed 's/^ //' <<'EOF' | sh
 set -eux
 wget https://my-bucket.storage.googleapis.com/timewarp
 chmod +x timewarp
 apk add git make
EOF
RUN sed 's/^ //' <<'EOF' | sh
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN sed 's/^ //' <<'EOF' >/build
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
RUN --mount=type=secret,id=auth_header sed 's/^ //' <<'EOF' | sh
 set -eux
 apk add curl && curl -O -H @/run/secrets/auth_header https://my-bucket.storage.googleapis.com/timewarp
 chmod +x timewarp
 apk add git make
EOF
RUN sed 's/^ //' <<'EOF' | sh
 set -eux
 ./timewarp -port 8080 &
 while ! nc -z localhost 8080;do sleep 1;done
 mkdir /src && cd /src
 git clone github.com/example .
 git checkout --force 'main'
 make deps ...
EOF
RUN sed 's/^ //' <<'EOF' >/build
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
		UseTimewarp:        false,
		UseNetworkProxy:    false,
		SaveContainerImage: true,
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

func TestGCBPlannerNoSaveWhenFlagFalse(t *testing.T) {
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
		SaveContainerImage: false,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
		},
	}

	plan, err := planner.GeneratePlan(ctx, input, opts)
	if err != nil {
		t.Fatalf("GeneratePlan failed: %v", err)
	}

	for _, step := range plan.Steps {
		if step.Script != "" && strings.Contains(step.Script, "docker save") {
			t.Error("Save step should not be present when SaveContainerImage is false")
		}
	}
}

func TestGCBPlannerBuildScriptWithSyscallMonitor(t *testing.T) {
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
		UseSyscallMonitor: true,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
			AssetStore:      rebuild.NewFilesystemAssetStore(memfs.New()),
		},
	}

	plan, err := planner.GeneratePlan(ctx, input, opts)
	if err != nil {
		t.Fatalf("GeneratePlan failed: %v", err)
	}

	var policyLines string
	for i, policy := range tetragonPoliciesYaml {
		policyLines += fmt.Sprintf(
			"cat > /workspace/tetragon/policy_%d.yaml <<EOPOLICY\n%sEOPOLICY\ndocker exec tetragon tetra tracingpolicy add /workspace/tetragon/policy_%d.yaml\n",
			i, policy, i)
	}

	want := textwrap.Dedent(`
			#!/usr/bin/env bash
			set -eux
			echo 'Starting rebuild for {Ecosystem:npm Package:test-package Version:1.0.0 Artifact:test-package-1.0.0.tgz}'
			touch /workspace/tetragon.jsonl
			mkdir -p /workspace/tetragon/
			export TID=$(docker run --name=tetragon --detach --pid=host --cgroupns=host --privileged -v=/workspace/tetragon.jsonl:/workspace/tetragon.jsonl -v=/workspace/tetragon/:/workspace/tetragon/ -v=/sys/kernel/btf/vmlinux:/var/lib/tetragon/btf quay.io/cilium/tetragon:v1.1.2 /usr/bin/tetragon --tracing-policy-dir=/workspace/tetragon/ --export-filename=/workspace/tetragon.jsonl --export-file-max-size-mb=2048)
			grep -q "Listening for events..." <(docker logs --follow $TID 2>&1) || (docker logs $TID && exit 1)
			TETRAGON_PID=$(docker inspect -f '{{.State.Pid}}' tetragon)
			`)[1:] + policyLines + textwrap.Dedent(`
			cat <<'EOS' | docker buildx build --tag=img -
			#syntax=docker/dockerfile:1.10
			FROM docker.io/library/alpine:3.19
			RUN sed 's/^ //' <<'EOF' | sh
			 set -eux
			 apk add git node npm
			EOF
			RUN sed 's/^ //' <<'EOF' | sh
			 set -eux
			 mkdir /src && cd /src
			 git clone github.com/example .
			 git checkout --force 'main'
			 npm install
			EOF
			RUN sed 's/^ //' <<'EOF' >/build
			 set -eux
			 npm run build
			 mkdir /out && cp /src/dist/test-package-1.0.0.tgz /out/
			EOF
			WORKDIR "/src"
			ENTRYPOINT ["/bin/sh","/build"]

			EOS
			docker run --name=container img
			docker stop -t 30 tetragon
			`)[1:]

	if diff := cmp.Diff(want, plan.Steps[0].Script); diff != "" {
		t.Errorf("build script mismatch (-want +got):\n%s", diff)
	}
}

func TestGCBPlannerSavePostBuildContainer(t *testing.T) {
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
		SavePostBuildContainer: true,
		Resources: build.Resources{
			BaseImageConfig: baseImageConfig,
			AssetStore:      rebuild.NewFilesystemAssetStore(memfs.New()),
			ToolURLs: map[build.ToolType]string{
				build.GSUtilTool: "https://storage.googleapis.com/test-bucket/gsutil_writeonly",
			},
		},
	}

	plan, err := planner.GeneratePlan(ctx, input, opts)
	if err != nil {
		t.Fatalf("GeneratePlan failed: %v", err)
	}

	if len(plan.Steps) != 4 {
		t.Fatalf("Expected 4 steps, got %d", len(plan.Steps))
	}

	// Skip step 0 (build) since its script embeds the full Dockerfile which is tested elsewhere.
	wantSteps := []*cloudbuild.BuildStep{
		{
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"cp", "container:/out/test-package-1.0.0.tgz", "/workspace/test-package-1.0.0.tgz"},
		},
		{
			Name:   "gcr.io/cloud-builders/docker",
			Script: "docker commit container container-postbuild && docker save container-postbuild | gzip > /workspace/image_postbuild.tgz && docker rmi container-postbuild",
		},
		{
			Name: "docker.io/library/alpine:3.19",
			Script: `set -eux
wget https://storage.googleapis.com/test-bucket/gsutil_writeonly
chmod +x gsutil_writeonly
./gsutil_writeonly cp /workspace/test-package-1.0.0.tgz file:///npm/test-package/1.0.0/test-package-1.0.0.tgz/test-package-1.0.0.tgz
./gsutil_writeonly cp /workspace/image_postbuild.tgz file:///npm/test-package/1.0.0/test-package-1.0.0.tgz/image_postbuild.tgz
`,
		},
	}
	if diff := cmp.Diff(wantSteps, plan.Steps[1:]); diff != "" {
		t.Errorf("Steps[1:] mismatch (-want +got):\n%s", diff)
	}
}
