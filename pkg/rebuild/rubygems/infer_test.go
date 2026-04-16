// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	reg "github.com/google/oss-rebuild/pkg/registry/rubygems"
)

func TestInferStrategy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		pkg            string
		version        string
		repoYAML       string
		versionDetail  string // JSON for /api/v2/rubygems/{name}/versions/{version}.json
		wantCommitID   string
		wantStrategyFn func(commitHash string) rebuild.Strategy
		wantErr        bool
	}{
		{
			name:    "basic",
			pkg:     "test-gem",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial
    tag: v1.0.0
    files:
      test-gem.gemspec: |
        Gem::Specification.new do |s|
          s.name = 'test-gem'
          s.version = '1.0.0'
        end
      lib/test_gem.rb: |
        module TestGem; end
`,
			versionDetail: `{"name":"test-gem","version":"1.0.0","platform":"ruby","sha":"abc","version_created_at":"2023-06-01T00:00:00Z"}`,
			wantCommitID:  "initial",
			wantStrategyFn: func(commitHash string) rebuild.Strategy {
				return &GemBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-gem",
						Ref:  commitHash,
					},
					RubyVersion:  "3.3.11",
					RegistryTime: must(parseTime("2023-06-01T00:00:00Z")),
				}
			},
		},
		{
			name:    "no matching tag",
			pkg:     "test-gem",
			version: "1.0.0",
			repoYAML: `commits:
  - id: initial
    files:
      test-gem.gemspec: |
        Gem::Specification.new do |s|
          s.name = 'test-gem'
          s.version = '1.0.0'
        end
`,
			versionDetail: `{"name":"test-gem","version":"1.0.0","platform":"ruby","sha":"abc","version_created_at":"2023-06-01T00:00:00Z"}`,
			wantErr:       true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, err := gitxtest.CreateRepoFromYAML(tc.repoYAML, nil)
			if err != nil {
				t.Fatalf("CreateRepoFromYAML: %v", err)
			}
			target := rebuild.Target{
				Ecosystem: rebuild.RubyGems,
				Package:   tc.pkg,
				Version:   tc.version,
			}
			target.Artifact = ArtifactName(target)
			rcfg := rebuild.RepoConfig{
				Repository: repo.Repository,
				URI:        "https://github.com/test-org/test-gem",
			}
			// Mock the registry: Artifact call (for gem spec) and Version call.
			client := httpxtest.MockClient{
				Calls: []httpxtest.Call{
					{
						URL: fmt.Sprintf("https://rubygems.org/gems/%s-%s.gem", tc.pkg, tc.version),
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(""), // Empty body; parseUpstreamGemSpec will fail gracefully
						},
					},
					{
						URL: fmt.Sprintf("https://rubygems.org/api/v2/rubygems/%s/versions/%s.json", tc.pkg, tc.version),
						Response: &http.Response{
							StatusCode: 200,
							Body:       httpxtest.Body(tc.versionDetail),
						},
					},
				},
				URLValidator: httpxtest.NewURLValidator(t),
			}
			mux := rebuild.RegistryMux{RubyGems: reg.HTTPRegistry{Client: &client}}
			s, err := Rebuilder{}.InferStrategy(ctx, target, mux, &rcfg, nil)
			if tc.wantErr {
				if err == nil {
					t.Errorf("InferStrategy expected error, got %v", s)
				}
				return
			}
			if err != nil {
				t.Fatalf("InferStrategy failed: %v", err)
			}
			commitHash := repo.Commits[tc.wantCommitID].String()
			want := tc.wantStrategyFn(commitHash)
			if diff := cmp.Diff(want, s); diff != "" {
				t.Errorf("InferStrategy mismatch (-want +got):\n%s", diff)
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

func parseTime(s string) (t time.Time, err error) {
	return time.Parse(time.RFC3339, s)
}
