// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestStrategies(t *testing.T) {
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		want     rebuild.Instructions
		wantErr  bool
	}{
		{
			"return maven build instructions for a valid JDK version",
			&MavenBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion:    "11.0.1",
				TargetVersion: "11",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				SystemDeps: []string{"git", "wget", "maven"},
				Source:     "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk11/13/GPL/openjdk-11.0.1_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk`)[1:],
				Build: textwrap.Dedent(`
					export JAVA_HOME=/opt/jdk
					export PATH=$JAVA_HOME/bin:$PATH
					mvn clean package -DskipTests --batch-mode -f dir -Dmaven.javadoc.skip=true -Dmaven.compiler.release=11`[1:]),
				OutputPath: "dir/target/ldapchai-0.8.6.jar",
			},
			false,
		},
		{
			"return gradle build instructions for a valid JDK version",
			&GradleBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion: "11.0.1",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				SystemDeps: []string{"git", "wget"},
				Source:     "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk11/13/GPL/openjdk-11.0.1_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk`)[1:],
				Build: textwrap.Dedent(`
					export JAVA_HOME=/opt/jdk
					export PATH=$JAVA_HOME/bin:$PATH
					./gradlew assemble --no-daemon --console=plain -Pversion=0.8.6`[1:]),
				OutputPath: "dir/build/libs/ldapchai-0.8.6.jar",
			},
			false,
		},
		{
			name: "use system-installed gradle if gradlew is not present",
			strategy: &GradleBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion:   "11.0.1",
				SystemGradle: gradleVersion,
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				SystemDeps: []string{"git", "wget", "zip"},
				Source:     "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk11/13/GPL/openjdk-11.0.1_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk
					wget -q -O tmp.zip https://services.gradle.org/distributions/gradle-8.14.3-bin.zip
					unzip -q tmp.zip -d /opt/ && mv /opt/gradle-8.14.3 /opt/gradle
					rm tmp.zip`)[1:],
				Build: textwrap.Dedent(`
					export JAVA_HOME=/opt/jdk
					export PATH=$JAVA_HOME/bin:$PATH
					export GRADLE_HOME=/opt/gradle
					export PATH=$GRADLE_HOME/bin:$PATH
					gradle assemble --no-daemon --console=plain -Pversion=0.8.6`[1:]),
				OutputPath: "dir/build/libs/ldapchai-0.8.6.jar",
			},
		},
		{
			"throw an error if JDK installation candidate is not found",
			&MavenBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "repo-dir",
				},
				JDKVersion: "30",
			},
			rebuild.Instructions{},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(rebuild.Target{Ecosystem: rebuild.Maven, Package: "com.github.ldapchai:ldapchai", Version: "0.8.6", Artifact: "ldapchai-0.8.6.jar"}, rebuild.BuildEnv{})
			if err != nil && !tc.wantErr {
				t.Fatalf("%s: Strategy%v.GenerateFor() failed unexpectedly: %v", tc.name, tc.strategy, err)
			}
			if tc.wantErr && (err == nil) {
				t.Fatalf("%s: Strategy%v.GenerateFor() should fail but did not", tc.name, tc.strategy)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("Strategy%v.GenerateFor() returned diff (-got +want):\n%s", tc.strategy, diff)
			}
		})
	}
}
