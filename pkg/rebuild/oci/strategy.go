// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// DockerfileBuild builds an image.tar from a Dockerfile.
type DockerfileBuild struct {
	rebuild.Location
	Dockerfile string `json:"dockerfile" yaml:"dockerfile"`
}

var _ rebuild.Strategy = &DockerfileBuild{}

// ToWorkflow converts the DockerfileBuild to a WorkflowStrategy.
func (s *DockerfileBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: s.Location,
		Build: []flow.Step{{
			Uses: "oci/build",
			With: map[string]string{
				"dockerfile": s.Dockerfile,
				"repo":       s.Repo,
				"ref":        s.Ref,
				"dir":        s.Dir,
			},
		}},
		Requires: rebuild.RequiredEnv{
			Privileged: true,
		},
		OutputPath: "image.tar",
	}
}

// GenerateFor generates the instructions for a DockerfileBuild.
func (s *DockerfileBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return s.ToWorkflow().GenerateFor(t, be)
}

func init() {
	flow.Tools.MustRegister(&flow.Tool{
		Name: "oci/build",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				sed 's/^ //' <<'DOCKERFILE_EOF' > Dockerfile
				{{.With.dockerfile | indent}}
				DOCKERFILE_EOF
				export DOCKER_BUILDKIT=1
				docker build -t rebuild \
					--build-arg REPO={{.With.repo}} \
					--build-arg REPO_REF={{.With.ref}} \
					--file Dockerfile \
					{{if .With.dir}}{{.With.dir}}{{else}}.{{end}}
				docker save rebuild -o image.tar`)[1:],
		}},
	})
}
