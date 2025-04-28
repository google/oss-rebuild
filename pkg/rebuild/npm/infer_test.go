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
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/npm"
)

func TestPickNodeVersion(t *testing.T) {
	tests := []struct {
		name        string
		nodeVersion string
		want        string
		wantErr     bool
	}{
		{
			name:        "empty version returns default",
			nodeVersion: "",
			want:        "10.17.0",
		},
		{
			name:        "exact version match",
			nodeVersion: "16.13.0",
			want:        "16.13.0",
		},
		{
			name:        "trust the future",
			nodeVersion: "24.6.1",
			want:        "24.6.1",
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
			got, err := PickNodeVersion(&reg.NPMVersion{NodeVersion: tt.nodeVersion})
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
	tests := []struct {
		name       string
		npmVersion string
		want       string
		wantErr    bool
	}{
		{
			name:       "empty version returns error",
			npmVersion: "",
			wantErr:    true,
		},
		{
			name:       "invalid semver returns error",
			npmVersion: "not.a.version",
			wantErr:    true,
		},
		{
			name:       "prerelease version returns error",
			npmVersion: "6.0.0-beta.1",
			wantErr:    true,
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
			got, err := PickNPMVersion(&reg.NPMVersion{NPMVersion: tt.npmVersion})
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
			wantCommitID:    "version-bump",
			wantStrategyFn: func(commitID string) rebuild.Strategy {
				return &NPMPackBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-package",
						Ref:  commitID,
						Dir:  ".",
					},
					NPMVersion: "8.1.2",
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
						Dir:  ".",
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
						Dir:  ".",
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
						Dir:  ".",
					},
					NPMVersion:        "8.2.0",
					NodeVersion:       "10.17.0",
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
						Dir:  ".",
					},
					NPMVersion:   "9.0.0",
					NodeVersion:  "10.17.0",
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
						Dir:  ".",
					},
					NPMVersion:        "6.2.0",
					NodeVersion:       "10.17.0",
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
						Dir:  ".",
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
			rcfg := rebuild.RepoConfig{
				Repository: repo.Repository,
				URI:        "https://github.com/test-org/test-package",
				Dir:        ".",
				RefMap:     map[string]string{"1.0.0": repo.Commits["version-bump"].String()},
			}
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
			s, err := Rebuilder{}.InferStrategy(ctx, target, mux, &rcfg, tc.locationHint)
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
