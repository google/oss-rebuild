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
				Runs: "mvn clean package -DskipTests --batch-mode -f {{.Location.Dir}} -Dmaven.javadoc.skip=true",
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

	deps := []flow.Step{{
		Uses: "maven/setup-java",
		With: map[string]string{
			"versionURL": jdkVersionURL,
		},
	}}
	if b.SystemGradle != "" {
		deps = append(deps, flow.Step{
			Uses: "maven/setup-gradle",
			With: map[string]string{
				"version": gradleVersion,
			},
		})
	}
	build := []flow.Step{{
		Uses: "maven/export-java",
	}}
	if b.SystemGradle != "" {
		build = append(build, flow.Step{
			Uses: "maven/export-gradle",
		})
		build = append(build, flow.Step{
			Uses: "maven/gradle-build",
		})
	} else {
		build = append(build, flow.Step{
			Uses: "maven/gradlew-build",
		})
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps:      deps,
		Build:     build,
		OutputDir: path.Join(b.Dir, "build", "libs"),
	}, nil
}

type GradleBuild struct {
	rebuild.Location
	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
	// SystemGradle indicates the version of Gradle to install instead of using the Gradle wrapper (gradlew).
	SystemGradle string `json:"system_gradle" yaml:"system_gradle"`
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
		Name: "maven/export-gradle",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				export GRADLE_HOME=/opt/gradle
				export PATH=$GRADLE_HOME/bin:$PATH`[1:]),
		}},
	},
	{
		Name: "maven/setup-gradle",
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
					wget -q -O tmp.zip https://services.gradle.org/distributions/gradle-{{.With.version}}-bin.zip
					unzip -q tmp.zip -d /opt/ && mv /opt/gradle-{{.With.version}} /opt/gradle
					rm tmp.zip`[1:]),
				Needs: []string{"wget", "zip"},
			},
		},
	},
	{
		Name: "maven/gradlew-build",
		Steps: []flow.Step{
			{
				Runs: "./gradlew assemble --no-daemon --console=plain -Pversion={{.Target.Version}} -x javadoc",
			},
		},
	},
	{
		Name: "maven/gradle-build",
		Steps: []flow.Step{
			{
				Runs: "gradle assemble --no-daemon --console=plain -Pversion={{.Target.Version}} -x javadoc",
			},
		},
	},
}
