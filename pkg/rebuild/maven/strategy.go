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
			Uses: "maven/setup-java",
			With: map[string]string{
				"versionURL": jdkVersionURL,
			},
		}},
		Build: []flow.Step{
			{
				Uses: "maven/export-java",
			},
			{
				// TODO: Java 9 needs additional certificate installed in /etc/ssl/certs/java/cacerts
				// It can be passed to maven command via -Djavax.net.ssl.trustStore=/etc/ssl/certs/java/cacerts
				Runs: "mvn clean package -DskipTests",
				// Note `maven` from apt also pull in jdk-21 and hence we must export JAVA_HOME and PATH in the step before
				Needs: []string{"maven"},
			},
		},
		OutputDir: "target/",
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
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
				mkdir -p /opt/jdk
				wget -q -O - "{{.With.versionURL}}" | tar -xzf - --strip-components=1 -C /opt/jdk`[1:]),
				Needs: []string{"wget"},
			},
		},
	},
	{
		Name: "maven/export-java",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				export JAVA_HOME=/opt/jdk
				export PATH=$JAVA_HOME/bin:$PATH`[1:]),
		}},
	},
}
