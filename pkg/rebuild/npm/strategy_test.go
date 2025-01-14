// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package npm

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"gopkg.in/yaml.v3"
)

func TestNPMCustomBuild(t *testing.T) {
	defaultLocation := rebuild.Location{
		Dir:  "the_dir",
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		want     rebuild.Instructions
	}{
		{
			"PackBuildVersionOverride",
			&NPMPackBuild{
				Location:        defaultLocation,
				NPMVersion:      "red",
				VersionOverride: "green",
			},
			rebuild.Instructions{
				Location:   defaultLocation,
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build: `PATH=/usr/bin:/bin:/usr/local/bin npm version --prefix the_dir --no-git-tag-version green
/usr/bin/npx --package=npm@red -c 'cd the_dir && npm pack'`,
				OutputPath: "the_dir/the_artifact",
			},
		},
		{
			"PackBuildNoVersionOverride",
			&NPMPackBuild{
				Location:        defaultLocation,
				NPMVersion:      "red",
				VersionOverride: "",
			},
			rebuild.Instructions{
				Location:   defaultLocation,
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build:      `/usr/bin/npx --package=npm@red -c 'cd the_dir && npm pack'`,
				OutputPath: "the_dir/the_artifact",
			},
		},
		{
			"PackBuildNoDir",
			&NPMPackBuild{
				Location: rebuild.Location{
					Dir:  ".",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				NPMVersion:      "red",
				VersionOverride: "",
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Dir:  ".",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build:      `/usr/bin/npx --package=npm@red -c 'npm pack'`,
				OutputPath: "the_artifact",
			},
		},
		{
			"CustomBuildVersionOverride",
			&NPMCustomBuild{
				Location:        defaultLocation,
				NPMVersion:      "red",
				NodeVersion:     "blue",
				VersionOverride: "green",
				Command:         "yellow",
				RegistryTime:    time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location:   defaultLocation,
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps: `/usr/bin/npm config --location-global set registry http://npm:2006-01-02T03:04:05Z@orange
trap '/usr/bin/npm config --location-global delete registry' EXIT
wget -O - https://unofficial-builds.nodejs.org/download/release/vblue/node-vblue-linux-x64-musl.tar.gz | tar xzf - --strip-components=1 -C /usr/local/
/usr/local/bin/npx --package=npm@red -c 'cd the_dir && npm install --force'`,
				Build: `PATH=/usr/bin:/bin:/usr/local/bin npm version --prefix the_dir --no-git-tag-version green
/usr/local/bin/npx --package=npm@red -c 'cd the_dir && npm run yellow && rm -rf node_modules && npm pack'`,
				OutputPath: "the_dir/the_artifact",
			},
		},
		{
			"CustomBuildNoVersionOverride",
			&NPMCustomBuild{
				Location:        defaultLocation,
				NPMVersion:      "red",
				NodeVersion:     "blue",
				VersionOverride: "",
				Command:         "yellow",
				RegistryTime:    time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location:   defaultLocation,
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps: `/usr/bin/npm config --location-global set registry http://npm:2006-01-02T03:04:05Z@orange
trap '/usr/bin/npm config --location-global delete registry' EXIT
wget -O - https://unofficial-builds.nodejs.org/download/release/vblue/node-vblue-linux-x64-musl.tar.gz | tar xzf - --strip-components=1 -C /usr/local/
/usr/local/bin/npx --package=npm@red -c 'cd the_dir && npm install --force'`,
				Build:      `/usr/local/bin/npx --package=npm@red -c 'cd the_dir && npm run yellow && rm -rf node_modules && npm pack'`,
				OutputPath: "the_dir/the_artifact",
			},
		},
		{
			"CustomBuildNoDir",
			&NPMCustomBuild{
				Location: rebuild.Location{
					Dir:  ".",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				NPMVersion:      "red",
				NodeVersion:     "blue",
				VersionOverride: "",
				Command:         "yellow",
				RegistryTime:    time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location: rebuild.Location{
					Dir:  ".",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				SystemDeps: []string{"git", "npm"},
				Source:     "git checkout --force 'the_ref'",
				Deps: `/usr/bin/npm config --location-global set registry http://npm:2006-01-02T03:04:05Z@orange
trap '/usr/bin/npm config --location-global delete registry' EXIT
wget -O - https://unofficial-builds.nodejs.org/download/release/vblue/node-vblue-linux-x64-musl.tar.gz | tar xzf - --strip-components=1 -C /usr/local/
/usr/local/bin/npx --package=npm@red -c 'npm install --force'`,
				Build:      `/usr/local/bin/npx --package=npm@red -c 'npm run yellow && rm -rf node_modules && npm pack'`,
				OutputPath: "the_artifact",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(rebuild.Target{Ecosystem: rebuild.NPM, Package: "the_package", Version: "the_version", Artifact: "the_artifact"}, rebuild.BuildEnv{TimewarpHost: "orange", HasRepo: true})
			if err != nil {
				t.Fatalf("%s: Strategy%v.GenerateFor() failed unexpectedly: %v", tc.name, tc.strategy, err)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("Strategy%v.GenerateFor() returned diff (-got +want):\n%s", tc.strategy, diff)
			}
		})
	}
}

func TestNPMPackBuildYAML(t *testing.T) {
	tests := []struct {
		name     string
		build    NPMPackBuild
		wantYAML string
	}{
		{
			name: "full config",
			build: NPMPackBuild{
				Location: rebuild.Location{
					Dir:  "test-dir",
					Repo: "https://example.com/test-repo",
					Ref:  "abc123",
				},
				NPMVersion:      "8.19.3",
				VersionOverride: "2.0.0",
			},
			wantYAML: `
location:
    repo: https://example.com/test-repo
    ref: abc123
    dir: test-dir
npm_version: 8.19.3
version_override: 2.0.0
`,
		},
		{
			name: "minimal config",
			build: NPMPackBuild{
				Location: rebuild.Location{
					Repo: "https://example.com/test-repo",
					Ref:  "abc123",
				},
				NPMVersion: "8.19.3",
			},
			wantYAML: `
location:
    repo: https://example.com/test-repo
    ref: abc123
npm_version: 8.19.3
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal the struct to YAML
			gotYAML, err := yaml.Marshal(tc.build)
			if err != nil {
				t.Fatalf("yaml.Marshal() error = %v", err)
			}

			// Compare generated YAML with expected YAML (normalizing whitespace)
			if diff := cmp.Diff(strings.TrimSpace(tc.wantYAML), strings.TrimSpace(string(gotYAML))); diff != "" {
				t.Errorf("YAML mismatch (-want +got):\n%s", diff)
			}

			// Unmarshal back to struct
			var gotBuild NPMPackBuild
			if err := yaml.Unmarshal(gotYAML, &gotBuild); err != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", err)
			}

			// Compare original struct with round-tripped struct
			if diff := cmp.Diff(tc.build, gotBuild); diff != "" {
				t.Errorf("Round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
