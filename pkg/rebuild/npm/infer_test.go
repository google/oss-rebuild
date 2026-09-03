// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/npm"
)

func TestPickNodeVersion(t *testing.T) {
	tests := []struct {
		name        string
		npmVersion  string
		nodeVersion string
		published   time.Time
		want        string
		wantErr     bool
	}{
		{
			name: "missing version before the table uses the earliest release",
			want: "10.16.0",
		},
		{
			name:      "missing version uses the newest LTS release at the publish time",
			published: must(time.Parse(time.DateOnly, "2023-10-07")),
			want:      "20.8.0",
		},
		{
			name:      "missing version skips the current line",
			published: must(time.Parse(time.DateOnly, "2020-11-01")),
			want:      "14.15.0",
		},
		{
			name:       "lerna user agent names the node",
			npmVersion: "lerna/3.22.1/node@v16.20.0+x64 (darwin)",
			want:       "16.20.0",
		},
		{
			name:        "exact version match",
			nodeVersion: "16.13.0",
			want:        "16.13.0",
		},
		{
			name:        "trust the future",
			nodeVersion: "99.0.0",
			want:        "99.0.0",
		},
		{
			name:        "node 8 upgrades to default",
			nodeVersion: "8.15.0",
			want:        "8.16.2",
		},
		{
			name:        "node 9 upgrades to 10",
			nodeVersion: "9.0.0",
			want:        "10.16.3",
		},
		{
			name:        "invalid semver returns error",
			nodeVersion: "not.a.version",
			want:        "",
			wantErr:     true,
		},
		{
			name:        "very old version falls back to appropriate default",
			nodeVersion: "6.0.0",
			want:        "8.16.2",
		},
		{
			name:        "handles non-MUSL versions correctly",
			nodeVersion: "13.10.0", // Exists but no MUSL
			want:        "13.10.1", // Exists and has MUSL
		},
		{
			name:        "non-existent defaults to highest patch version of next highest release",
			nodeVersion: "14.14.1",
			want:        "14.15.5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PickNodeVersion(&reg.NPMVersion{NPMVersion: tt.npmVersion, NodeVersion: tt.nodeVersion}, tt.published)
			if (err != nil) != tt.wantErr {
				t.Errorf("PickNodeVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("PickNodeVersion() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPickNPMVersion(t *testing.T) {
	published := must(time.Parse(time.DateOnly, "2023-10-07"))
	tests := []struct {
		name        string
		npmVersion  string
		nodeVersion string
		published   time.Time
		want        string
		wantErr     bool
	}{
		{
			name: "missing versions before the table use the earliest release",
			want: "6.9.0",
		},
		{
			name:       "invalid version uses the earliest release before the table",
			npmVersion: "not.a.version",
			want:       "6.9.0",
		},
		{
			name:       "prerelease version maps to its release",
			npmVersion: "6.0.0-beta.1",
			want:       "6.0.0",
		},
		{
			name:       "next prerelease maps to its release",
			npmVersion: "6.12.0-next.0",
			want:       "6.12.0",
		},
		{
			name:       "unpublished 6.9.1 upgrades to 6.9.2",
			npmVersion: "6.9.1-next.0",
			want:       "6.9.2",
		},
		{
			name:       "canary prerelease applies the npm 5 upgrade",
			npmVersion: "5.5.1-canary.5",
			want:       "5.6.0",
		},
		{
			name:       "prerelease below 5.x upgrades to 5.0.4",
			npmVersion: "1.1.0-beta-4",
			want:       "5.0.4",
		},
		{
			name:       "user agent without a node uses the publish time",
			npmVersion: "ethers-dist@0.0.1",
			published:  published,
			want:       "10.1.0",
		},
		{
			name:       "lerna user agent uses the npm bundled with its node",
			npmVersion: "lerna/3.22.1/node@v16.20.0+x64 (darwin)",
			want:       "8.19.4",
		},
		{
			name:        "lerna user agent takes precedence over the node version field",
			npmVersion:  "lerna/4.11.5/node@v26.8.1+arm64 (darwin)",
			nodeVersion: "12.22.12",
			want:        "11.19.0",
		},
		{
			name:       "lerna user agent with node below the table uses the next release",
			npmVersion: "lerna/3.20.2/node@v10.15.0+x64 (darwin)",
			want:       "6.9.0",
		},
		{
			name:        "missing npm version uses the npm bundled with its node",
			nodeVersion: "18.5.0",
			want:        "8.12.1",
		},
		{
			name:        "missing npm version with node above the table uses the publish time",
			nodeVersion: "99.0.0",
			published:   published,
			want:        "10.1.0",
		},
		{
			name:        "missing npm version with invalid node version returns error",
			nodeVersion: "not.a.version",
			published:   published,
			wantErr:     true,
		},
		{
			name:      "missing npm and node versions use the publish time",
			published: published,
			want:      "10.1.0",
		},
		{
			name:      "publish time uses the npm bundled with the newest LTS release",
			published: must(time.Parse(time.DateOnly, "2020-11-01")),
			want:      "6.14.8",
		},
		{
			name:      "publish time before the table uses the earliest release",
			published: must(time.Parse(time.DateOnly, "2018-07-29")),
			want:      "6.9.0",
		},
		{
			name:       "build tag version returns error",
			npmVersion: "6.0.0+20200101",
			wantErr:    true,
		},
		{
			name:       "less than version 5.x upgrades to 5.0.4",
			npmVersion: "4.2.0",
			want:       "5.0.4",
		},
		{
			name:       "version 5.4.x upgrades to 5.6.0",
			npmVersion: "5.4.2",
			want:       "5.6.0",
		},
		{
			name:       "version 5.5.x upgrades to 5.6.0",
			npmVersion: "5.5.1",
			want:       "5.6.0",
		},
		{
			name:       "version 5.3.x stays as is",
			npmVersion: "5.3.0",
			want:       "5.3.0",
		},
		{
			name:       "version 5.6.x stays as is",
			npmVersion: "5.6.0",
			want:       "5.6.0",
		},
		{
			name:       "version 6.x stays as is",
			npmVersion: "6.14.8",
			want:       "6.14.8",
		},
		{
			name:       "version 7.x stays as is",
			npmVersion: "7.0.0",
			want:       "7.0.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PickNPMVersion(&reg.NPMVersion{NPMVersion: tt.npmVersion, NodeVersion: tt.nodeVersion}, tt.published)
			if (err != nil) != tt.wantErr {
				t.Errorf("PickNPMVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("PickNPMVersion() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInferStrategy_NPM(t *testing.T) {
	for _, tc := range []struct {
		name            string
		pkg             string
		version         string
		repoYAML        string
		versionMetadata string
		packageMetadata string
		locationHint    *rebuild.LocationHint
		wantCommitID    string
		wantStrategyFn  func(commitID string) rebuild.Strategy
		wantErr         bool
	}{
		{
			name:    "NPMPackBuild - ref from gitHead",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "0.9.0"}
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0"}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"8.1.2","dist":{"tarball":"url1"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-01-01T12:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion: "8.1.2",
				}
			},
		},
		{
			name:    "NPMPackBuild - npm version from publish time",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0"}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","dist":{"tarball":"url1"},"gitHead":"INSERT_COMMIT_ID"}`, // No _npmVersion
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-10-07T12:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion: "10.1.0",
				}
			},
		},
		{
			name:    "NPMPackBuild - ref from tag",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: tagged-commit
    parent: initial-commit
    tag: v1.0.0
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0"}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"7.5.0","dist":{"tarball":"url2"}}`, // No gitHead
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-01-01T12:00:00.000Z"}}`,
			wantCommitID:    "tagged-commit",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion: "7.5.0",
				}
			},
		},
		{
			name:    "NPMPackBuild - ref from refmap",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0"}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"6.14.0","dist":{"tarball":"url3"}}`, // No gitHead, no relevant tag in YAML
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-01-01T12:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion: "6.14.0",
				}
			},
		},
		{
			name:    "NPMCustomBuild - from build script",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0", "scripts": {"build": "tsc"}}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"8.2.0","nodeVersion": "16.13.0", "dist":{"tarball":"url4"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-02-10T10:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMCustomBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion:        "8.2.0",
					NodeVersion:       "18.14.0",
					Command:           "build",
					RegistryTime:      must(time.Parse(time.RFC3339, "2023-02-10T10:00:00.000Z")),
					PrepackRemoveDeps: true,
				}
			},
		},
		{
			name:    "NPMCustomBuild - rely on implicit prepare script",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0", "scripts": {"prepare": "npm run compile"}}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"9.0.0","nodeVersion": "18.0.0", "dist":{"tarball":"url5"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-03-01T11:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMCustomBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion:   "9.0.0",
					NodeVersion:  "18.14.2",
					RegistryTime: must(time.Parse(time.RFC3339, "2023-03-01T11:00:00.000Z")),
				}
			},
		},
		{
			name:    "NPMCustomBuild - from build script",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0", "scripts": {"build": "tsc"}}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"6.2.0","nodeVersion": "16.13.0", "dist":{"tarball":"url4"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-02-10T10:00:00.000Z"}}`,
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMCustomBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion:        "6.2.0",
					NodeVersion:       "18.14.0",
					Command:           "build",
					RegistryTime:      must(time.Parse(time.RFC3339, "2023-02-10T10:00:00.000Z")),
					PrepackRemoveDeps: true,
					KeepRoot:          true,
				}
			},
		},
		{
			name:    "Error - unreadable package.json in commit",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      package.json: "this is not json" # Invalid package.json in the repo commit
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"8.0.0","dist":{"tarball":"url6"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-01-01T12:00:00.000Z"}}`,
			wantErr:         true,
		},
		{
			name:    "Error - missing upload time for custom build",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: version-bump
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0", "scripts": {"build": "echo build"}}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"8.0.0","dist":{"tarball":"url7"},"gitHead":"INSERT_COMMIT_ID"}`,
			packageMetadata: `{"name":"test-package","time":{}}`, // Missing time for "1.0.0"
			wantErr:         true,
		},
		{
			name:    "Hint usage - NPMPackBuild",
			pkg:     "test-package",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial-commit
  - id: version-bump
    parent: initial-commit
    files:
      pkg/package.json: |
        {"name": "test-package", "version": "1.0.0"}
  - id: other-commit
    parent: initial-commit
    tag: v1.0.0
    files:
      package.json: |
        {"name": "test-package", "version": "1.0.0"}
`,
			versionMetadata: `{"name":"test-package","version":"1.0.0","_npmVersion":"8.1.2","dist":{"tarball":"url1"}}`,
			packageMetadata: `{"name":"test-package","time":{"1.0.0":"2023-01-01T12:00:00.000Z"}}`,
			locationHint: &rebuild.LocationHint{
				Location: rebuild.Location{
					Repo: "https://github.com/test-org/test-package",
					Ref:  "INSERT_COMMIT_ID",
				},
			},
			wantCommitID: "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  "",
					},
					NPMVersion: "8.1.2",
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))
			targetCommitID := repo.Commits[tc.wantCommitID].String()
			tc.versionMetadata = strings.ReplaceAll(tc.versionMetadata, "INSERT_COMMIT_ID", targetCommitID)
			if tc.locationHint != nil {
				tc.locationHint.Location.Ref = strings.ReplaceAll(tc.locationHint.Location.Ref, "INSERT_COMMIT_ID", targetCommitID)
			}
			target := rebuild.Target{
				Ecosystem: rebuild.NPM,
				Package:   tc.pkg,
				Version:   tc.version,
			}
			target.Artifact = ArtifactName(target)
			client := httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						URL: "https://registry.npmjs.org/" + tc.pkg + "/" + tc.version,
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(tc.versionMetadata),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			if tc.packageMetadata != "" {
				client.Calls = append(client.Calls, httpxtest.Call{
					URL: "https://registry.npmjs.org/" + tc.pkg,
					Response: &http.Response{
						StatusCode: 200,
						Body:       httpxtest.Body(tc.packageMetadata),
					},
				})
			}
			mux := rebuild.RegistryMux{NPM: &reg.HTTPRegistry{Client: &client}}
			s, err := Rebuilder{}.InferStrategy(ctx, target, mux, &rebuild.RepoConfig{
				Repo:   gitx.Repo{Repository: repo.Repository},
				URI:    "https://github.com/test-org/test-package",
				Dir:    "",
				RefMap: map[string]string{"1.0.0": repo.Commits["version-bump"].String()},
			}, tc.locationHint)
			if tc.wantErr {
				if err == nil {
					t.Errorf("InferStrategy expected error, got %v", s)
				}
			} else if err != nil {
				t.Fatalf("InferStrategy failed: %v", err)
			} else {
				if tc.wantStrategyFn == nil {
					t.Fatal("tc.wantFn is nil but no error was expected")
				}
				want := tc.wantStrategyFn(targetCommitID)
				if diff := cmp.Diff(want, s); diff != "" {
					t.Errorf("InferStrategy mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
