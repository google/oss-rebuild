// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestMavenStrategies(t *testing.T) {
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		want     rebuild.Instructions
		wantErr  bool
	}{
		{
			"MavenBuildLDAPChai",
			&MavenBuild{
				Location: rebuild.Location{
					Repo: "https://github.com/ldapchai/ldapchai.git",
					Ref:  "a9de4ccc8db9a4862f3819f3dfb63e57a6450bdf",
					Dir:  "ldapchai",
				},
				JDKVersion: "8",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Repo: "https://github.com/ldapchai/ldapchai.git",
					Ref:  "a9de4ccc8db9a4862f3819f3dfb63e57a6450bdf",
					Dir:  "ldapchai",
				},
				SystemDeps: []string{"git", "wget"},
				Source:     "git clone https://github.com/ldapchai/ldapchai.git .\ngit checkout --force 'a9de4ccc8db9a4862f3819f3dfb63e57a6450bdf'",
				Deps: `mkdir -p /opt/jdk
wget -q -O - "https://github.com/adoptium/temurin8-binaries/releases/download/jdk8u462-b08/OpenJDK8U-jdk_x64_linux_hotspot_8u462b08.tar.gz" | tar -xzf - --strip-components=1 -C /opt/jdk
export JAVA_HOME=/opt/jdk
export PATH=$JAVA_HOME/bin:$PATH`,
				Build:      "echo 'Building Maven project'", // TODO: Replace with actual Maven build command
				OutputPath: "ldapchai-0.8.6.jar",
			},
			false,
		},
		{
			"InvalidJDK",
			&MavenBuild{
				Location: rebuild.Location{
					Repo: "https://foo.bar",
					Ref:  "invalid-ref",
					Dir:  "invalid-dir",
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
