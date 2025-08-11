// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"github.com/google/oss-rebuild/internal/textwrap"

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
				"versionURL": getVersionURL(b.JDKVersion),
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
			Runs: textwrap.Dedent(`mkdir -p /opt/jdk
wget -q -O - "{{.With.versionURL}}" | tar -xzf - --strip-components=1 -C /opt/jdk
export JAVA_HOME=/opt/jdk
export PATH=$JAVA_HOME/bin:$PATH`),
			Needs: []string{"wget"},
		}},
	},
	{
		Name: "maven/deps/basic",
		Steps: []flow.Step{
			{
				Uses: "maven/setup-java",
				With: map[string]string{
					"versionURL": "{{.With.versionURL}}",
				},
			},
		},
	},
}

func getVersionURL(version string) string {
	url, exists := JDKDownloadURLs[version]
	if exists {
		return url
	}
	return ""
}
