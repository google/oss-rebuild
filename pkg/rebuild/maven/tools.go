// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
)

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
		Name: "maven/maven-build",
		Steps: []flow.Step{{
			// TODO: Java 9 needs additional certificate installed in /etc/ssl/certs/java/cacerts
			// It can be passed to maven command via -Djavax.net.ssl.trustStore=/etc/ssl/certs/java/cacerts
			Runs: "mvn clean package -DskipTests",
			// Note `maven` from apt also pull in jdk-21 and hence we must export JAVA_HOME and PATH in the step before
			Needs: []string{"maven"},
		}},
	},
	{
		Name: "maven/gradle-build",
		Steps: []flow.Step{{
			Runs: "./gradlew build -x test --no-daemon",
		}},
	},
	{
		Name: "maven/move-gradle-build-output",
		Steps: []flow.Step{{
			Runs: "find . -name {{.With.targetName}} -type f -exec mv {} /src/ \\;",
		}},
	},
}
