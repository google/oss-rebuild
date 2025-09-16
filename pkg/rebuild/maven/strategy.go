// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"path"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
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
				Runs: "mvn clean package -DskipTests --batch-mode",
				// Note `maven` from apt also pull in jdk-21 and hence we must export JAVA_HOME and PATH in the step before
				Needs: []string{"maven"},
			},
		},
		OutputDir: path.Join(b.Dir, "target"),
	}, nil
}

func (b *MavenBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	workflow, err := b.ToWorkflow()
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return workflow.GenerateFor(t, be)
}

func (b *GradleBuild) ToWorkflow() (*rebuild.WorkflowStrategy, error) {
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
			Needs: []string{"curl"},
		}},
		Build: []flow.Step{
			{
				Uses: "maven/export-java",
			},
			{
				Uses: "maven/setup-gradle",
			},
			{
				// We default to using Gradle's wrapper if it exists, otherwise we use the system-installed Gradle.
				// We run assemble as it is an atomic lifecycle task that outputs the artifact.
				// The property `-Pversion` is used to set the project version which ensures that the right version is appended to the artifact name.
				Runs: textwrap.Dedent(`
				if [ -f gradlew ]; then
					./gradlew assemble --no-daemon --console=plain -Pversion={{.Target.Version}};
				else
					gradle assemble --no-daemon --console=plain -Pversion={{.Target.Version}};
				fi`)[1:],
			},
		},
		OutputDir: path.Join(b.Dir, "build", "libs"),
	}, nil
}

type GradleBuild struct {
	rebuild.Location

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
}

var _ rebuild.Strategy = &GradleBuild{}

func (b *GradleBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
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
	{
		Name: "maven/setup-gradle",
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
				if [ ! -f gradlew ]; then
					wget -q -O gradle-8.14.3-bin.zip https://services.gradle.org/distributions/gradle-8.14.3-bin.zip
					unzip -q gradle-8.14.3-bin.zip -d /opt/
					rm gradle-8.14.3-bin.zip
					export GRADLE_HOME=/opt/gradle-8.14.3
					export PATH=$GRADLE_HOME/bin:$PATH
				fi`[1:]),
				Needs: []string{"zip", "wget"},
			},
		},
	},
}
