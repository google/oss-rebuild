// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"strconv"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type MavenBuild struct {
	rebuild.Location

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
}

var _ rebuild.Strategy = &MavenBuild{}

func (b *MavenBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "maven/deps/basic",
			With: map[string]string{
				"version": getOnlyMajorVersion(b.JDKVersion),
			},
		}},
		Build: []flow.Step{{
			Runs: "echo 'Building Maven project'",
		}},
	}
}

func (b *MavenBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var toolkit = []*flow.Tool{
	{
		Name: "maven/setup-java",
		Steps: []flow.Step{{
			Runs: "apk add openjdk{{.With.version}}",
		}},
	},
	{
		Name: "maven/setup-maven",
		Steps: []flow.Step{{
			Runs: "apk add maven",
		}},
	},
	{
		Name: "maven/deps/basic",
		Steps: []flow.Step{
			{
				Uses: "maven/setup-java",
				With: map[string]string{
					"version": "{{.With.version}}",
				},
			},
			{
				Uses: "maven/setup-maven",
			},
		},
	},
}

func getOnlyMajorVersion(version string) string {
	if major, err := strconv.Atoi(version); err == nil {
		return strconv.Itoa(major)
	}
	parts := strings.Split(version, ".")
	return parts[0]
}
