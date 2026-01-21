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
				JDKVersion: "12",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "wget", "maven"},
				},
				Source: "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk12/33/GPL/openjdk-12_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk`)[1:],
				Build: textwrap.Dedent(`
					export JAVA_HOME=/opt/jdk
					export PATH=$JAVA_HOME/bin:$PATH
					mvn clean package -DskipTests --batch-mode -f dir -Dmaven.javadoc.skip=true`[1:]),
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
				JDKVersion: "13",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "wget"},
				},
				Source: "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk13/5b8a42f3905b406298b72d750b6919f6/33/GPL/openjdk-13_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk`)[1:],
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
				JDKVersion:   "13",
				SystemGradle: gradleVersion,
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "wget", "zip"},
				},
				Source: "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/GA/jdk13/5b8a42f3905b406298b72d750b6919f6/33/GPL/openjdk-13_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk
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
			name: "maven build instructions for JDK 10",
			strategy: &MavenBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion: "10",
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "wget", "ca-certificates", "maven"},
				},
				Source: "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
                    mkdir -p /opt/jdk
                    wget -q -O - "https://download.java.net/java/GA/jdk10/10/binaries/openjdk-10_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk
                    export JAVA_HOME=/opt/jdk
                    export PATH=$JAVA_HOME/bin:$PATH
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
                    done`)[1:],
				Build: textwrap.Dedent(`
                    export JAVA_HOME=/opt/jdk
                    export PATH=$JAVA_HOME/bin:$PATH
                    mvn clean package -DskipTests --batch-mode -f dir -Dmaven.javadoc.skip=true`[1:]),
				OutputPath: "dir/target/ldapchai-0.8.6.jar",
			},
			wantErr: false,
		},
		{
			name: "gradle build instructions for JDK 9",
			strategy: &GradleBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion: "9",
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "wget", "ca-certificates"},
				},
				Source: "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
                    mkdir -p /opt/jdk
                    wget -q -O - "https://download.java.net/java/GA/jdk9/9/binaries/openjdk-9_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk
                    export JAVA_HOME=/opt/jdk
                    export PATH=$JAVA_HOME/bin:$PATH
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
                    done`)[1:],
				Build: textwrap.Dedent(`
                    export JAVA_HOME=/opt/jdk
                    export PATH=$JAVA_HOME/bin:$PATH
                    ./gradlew assemble --no-daemon --console=plain -Pversion=0.8.6`[1:]),
				OutputPath: "dir/build/libs/ldapchai-0.8.6.jar",
			},
			wantErr: false,
		},
		{
			name: "enforce TLS 1.2 for JDK 11.0.1 in maven build",
			strategy: &MavenBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion: "11.0.1",
			},
			want: rebuild.Instructions{
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
					mvn clean package -DskipTests --batch-mode -f dir -Dmaven.javadoc.skip=true -Djdk.tls.client.protocols="TLSv1.2"`[1:]),
				OutputPath: "dir/target/ldapchai-0.8.6.jar",
			},
		},
		{
			name: "enforce TLS 1.2 for JDK 11 in gradle build",
			strategy: &GradleBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				JDKVersion: "11",
			},
			want: rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "ref",
					Dir:  "dir",
				},
				SystemDeps: []string{"git", "wget"},
				Source:     "git clone https://foo.bar .\ngit checkout --force 'ref'",
				Deps: textwrap.Dedent(`
					mkdir -p /opt/jdk
					wget -q -O - "https://download.java.net/java/ga/jdk11/openjdk-11_linux-x64_bin.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk`)[1:],
				Build: textwrap.Dedent(`
					export JAVA_HOME=/opt/jdk
					export PATH=$JAVA_HOME/bin:$PATH
					./gradlew assemble --no-daemon --console=plain -Pversion=0.8.6 -Djdk.tls.client.protocols="TLSv1.2"`[1:]),
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
