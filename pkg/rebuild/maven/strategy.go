// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"cmp"
	"log"
	"path"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type MavenBuild struct {
	rebuild.Location

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
	// MavenVersion is the Maven release to install. Empty installs defaultMaven.
	MavenVersion string `json:"maven_version,omitempty" yaml:"maven_version,omitempty"`
	// CRLF checks the source out with CRLF line endings, as on Windows.
	CRLF bool `json:"crlf,omitempty" yaml:"crlf,omitempty"`
}

var _ rebuild.Strategy = &MavenBuild{}

func (b *MavenBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	jdkVersionURL, exists := JDKDownloadURLs[b.JDKVersion]
	if !exists {
		// NOTE: There is no error outlet for rebuild.Flowable but we can
		// return an invalid URL if we aren't aware of how to locate the JDK.
		log.Printf("Warning: no download URL for JDK version %s; add it to JDKDownloadURLs", b.JDKVersion)
		jdkVersionURL = "http://not-found.invalid/"
	}
	deps := []flow.Step{{
		Uses: "maven/setup-java",
		With: map[string]string{
			"versionURL": jdkVersionURL,
		},
	}}
	// Java 9 does not ship certificates under /lib/security/cacerts so we need to manually add them.
	// Reference: https://www.oracle.com/java/technologies/javase/9-all-relnotes.html#JDK-8189131
	// Java 10 onwards the certificates are shipped but they don't complete the chain.
	if strings.HasPrefix(b.JDKVersion, "9") || strings.HasPrefix(b.JDKVersion, "10") {
		deps = append(deps, flow.Step{
			Uses: "maven/export-java",
		})
		deps = append(deps, flow.Step{
			Uses: "maven/regen-cacerts",
		})
	}
	deps = append(deps, flow.Step{
		Uses: "maven/setup-maven",
		With: map[string]string{"version": cmp.Or(b.MavenVersion, defaultMaven)},
	})
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source:   checkout(b.CRLF),
		Deps:     deps,
		Build: []flow.Step{
			{Uses: "maven/export-java"},
			{Uses: "maven/export-maven"},
			{Uses: "maven/mvn-build", With: map[string]string{"dir": cmp.Or(b.Dir, ".")}},
		},
		OutputDir: path.Join(b.Dir, "target"),
	}
}

// defaultMaven is the Maven release for strategies without maven_version. It
// matches the apt package they used before (Debian 13).
const defaultMaven = "3.9.9"

// checkout is the source step, optionally converting line endings to CRLF.
func checkout(crlf bool) []flow.Step {
	step := flow.Step{Uses: "git-checkout"}
	if crlf {
		step.With = map[string]string{"crlf": "true"}
	}
	return []flow.Step{step}
}

func (b *MavenBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func (b *GradleBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	jdkVersionURL, exists := JDKDownloadURLs[b.JDKVersion]
	if !exists {
		// NOTE: There is no error outlet for rebuild.Flowable but we can
		// return an invalid URL if we aren't aware of how to locate the JDK.
		log.Printf("Warning: no download URL for JDK version %s; add it to JDKDownloadURLs", b.JDKVersion)
		jdkVersionURL = "http://not-found.invalid/"
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
				"version": b.SystemGradle,
			},
		})
	}
	// Java 9 does not ship certificates under /lib/security/cacerts so we need to manually add them.
	// Reference: https://www.oracle.com/java/technologies/javase/9-all-relnotes.html#JDK-8189131
	// Java 10 onwards the certificates are shipped but they don't complete the chain.
	if strings.HasPrefix(b.JDKVersion, "9") || strings.HasPrefix(b.JDKVersion, "10") {
		deps = append(deps, flow.Step{
			Uses: "maven/export-java",
		})
		deps = append(deps, flow.Step{
			Uses: "maven/regen-cacerts",
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
	}
}

type GradleBuild struct {
	rebuild.Location
	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
	// SystemGradle indicates the version of Gradle to install instead of using the Gradle wrapper (gradlew).
	SystemGradle string `json:"system_gradle" yaml:"system_gradle"`
}

var _ rebuild.Strategy = &GradleBuild{}

// gradleDistributionVersion matches release, release candidate and milestone
// versions published at https://services.gradle.org/distributions/ (listed at
// https://services.gradle.org/versions/all), e.g. 8.14.3, 7.6, 8.0-rc-1 and
// 8.0-milestone-1. Nightly snapshots and pre-1.0 previews are not accepted.
var gradleDistributionVersion = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(-(rc|milestone)-\d+)?$`)

func (b *GradleBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	if b.SystemGradle != "" && !gradleDistributionVersion.MatchString(b.SystemGradle) {
		return rebuild.Instructions{}, errors.Errorf("invalid system Gradle version %q", b.SystemGradle)
	}
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
			// Without a UTF-8 locale, javac reads sources and writes class file
			// names as ASCII, unlike a publisher's desktop.
			Runs: textwrap.Dedent(`
				export JAVA_HOME=/opt/jdk
				export PATH=$JAVA_HOME/bin:$PATH
				export LANG=C.UTF-8`[1:]),
		}},
	},
	{
		Name: "maven/setup-maven",
		Steps: []flow.Step{
			{
				// Maven before 3.2.3 reaches Central over plain HTTP, which Central
				// refuses, so the global settings mirror it to the HTTPS URL.
				Runs: textwrap.Dedent(`
					mkdir -p /opt/maven
					wget -q -O - "https://repo1.maven.org/maven2/org/apache/maven/apache-maven/{{.With.version}}/apache-maven-{{.With.version}}-bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/maven
					cat <<'SETTINGS' > /opt/maven/conf/settings.xml
					<settings><mirrors><mirror><id>central-https</id><mirrorOf>central</mirrorOf><url>https://repo.maven.apache.org/maven2</url></mirror></mirrors></settings>
					SETTINGS`[1:]),
				Needs: []string{"wget"},
			},
		},
	},
	{
		Name: "maven/export-maven",
		Steps: []flow.Step{{
			Runs: "export PATH=/opt/maven/bin:$PATH",
		}},
	},
	{
		Name: "maven/mvn-build",
		Steps: []flow.Step{{
			Runs: "mvn clean package -DskipTests --batch-mode -f {{.With.dir}} -Dmaven.javadoc.skip=true",
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
				Runs: "./gradlew assemble --no-daemon --console=plain -Pversion={{.Target.Version}}",
			},
		},
	},
	{
		Name: "maven/gradle-build",
		Steps: []flow.Step{
			{
				Runs: "gradle assemble --no-daemon --console=plain -Pversion={{.Target.Version}}",
			},
		},
	},
	{
		Name: "maven/regen-cacerts",
		Steps: []flow.Step{
			{
				// We remove the existing cacerts file since keytool throws an error `Keystore was tampered with, or password was incorrect` if we try to modify it.
				// We set the locale to UTF-8 to properly handle non-ASCII characters in certificate filenames.
				// Notice that we set the locale only in the subshell where we run the while loop to avoid affecting rest of the script.
				// One example is /etc/ssl/certs/NetLock_Arany_=Class_Gold=_Főtanúsítvány.pem
				Runs: textwrap.Dedent(`
                    KEYSTORE_FILE="$JAVA_HOME/lib/security/cacerts"
                    rm -f $KEYSTORE_FILE
                    find /etc/ssl/certs -name '*.pem' | while read cert_path; do
                      export LANG=C.UTF-8
                      keytool -importcert -noprompt \
                        -keystore "$KEYSTORE_FILE" \
                        -alias "$(basename "$cert_path")" \
                        -file "$cert_path" \
                        -storepass password \
                        -storetype JKS
                    done`[1:]),
				Needs: []string{"ca-certificates"},
			},
		},
	},
}
