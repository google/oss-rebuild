// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/pkg/errors"

	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type MavenBuild struct {
	rebuild.Location

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
}

var _ rebuild.Strategy = &MavenBuild{}

func (b *MavenBuild) ToWorkflow() (*rebuild.WorkflowStrategy, error) {
	jdkVersionURL, exists := JDKDownloadURLs[b.JDKVersion]
	if !exists {
		return nil, errors.Errorf("no download URL for JDK version %s", b.JDKVersion)
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "maven/deps/basic",
			With: map[string]string{
				"versionURL": jdkVersionURL,
			},
		}},
		Build: []flow.Step{{
			Runs: "echo 'Building Maven project'",
		}},
	}, nil
}

func (b *MavenBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	workflow, err := b.ToWorkflow()
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return workflow.GenerateFor(t, be)
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
			// TODO: ensure that the environment variables JAVA_HOME and PATH are also available for the build step of strategy
			Runs: textwrap.Dedent(`
				mkdir -p /opt/jdk
				wget -q -O - "{{.With.versionURL}}" | tar -xzf - --strip-components=1 -C /opt/jdk
				export JAVA_HOME=/opt/jdk
				export PATH=$JAVA_HOME/bin:$PATH`[1:]),
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
