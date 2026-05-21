// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDockerfileBuild_GenerateFor(t *testing.T) {
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		target   rebuild.Target
		buildEnv rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "basic config",
			strategy: &DockerfileBuild{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo",
					Ref:  "abcdef",
					Dir:  "subdir",
				},
				Dockerfile: "FROM alpine\nRUN echo hello",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.OCI,
				Package:   "test-package",
				Version:   "1.0.0",
				Artifact:  "image.tar",
			},
			buildEnv: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo",
					Ref:  "abcdef",
					Dir:  "subdir",
				},
				Source: "",
				Deps:   "",
				Build: `sed 's/^ //' <<'DOCKERFILE_EOF' > Dockerfile
 FROM alpine
 RUN echo hello
DOCKERFILE_EOF
export DOCKER_BUILDKIT=1
docker build -t rebuild \
	--build-arg REPO=https://github.com/example/repo \
	--build-arg REPO_REF=abcdef \
	--file Dockerfile \
	subdir
docker save rebuild -o image.tar`,
				Requires: rebuild.RequiredEnv{
					Privileged: true,
				},
				OutputPath: "image.tar",
			},
		},
		{
			name: "empty dir",
			strategy: &DockerfileBuild{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo",
					Ref:  "abcdef",
					Dir:  "",
				},
				Dockerfile: "FROM alpine",
			},
			target: rebuild.Target{
				Ecosystem: rebuild.OCI,
				Package:   "test-package",
				Version:   "1.0.0",
				Artifact:  "image.tar",
			},
			buildEnv: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/example/repo",
					Ref:  "abcdef",
					Dir:  "",
				},
				Source: "",
				Deps:   "",
				Build: `sed 's/^ //' <<'DOCKERFILE_EOF' > Dockerfile
 FROM alpine
DOCKERFILE_EOF
export DOCKER_BUILDKIT=1
docker build -t rebuild \
	--build-arg REPO=https://github.com/example/repo \
	--build-arg REPO_REF=abcdef \
	--file Dockerfile \
	.
docker save rebuild -o image.tar`,
				Requires: rebuild.RequiredEnv{
					Privileged: true,
				},
				OutputPath: "image.tar",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(tc.target, tc.buildEnv)
			if err != nil {
				t.Fatalf("GenerateFor failed: %v", err)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("GenerateFor mismatch (-got +want):\n%s", diff)
			}
		})
	}
}
