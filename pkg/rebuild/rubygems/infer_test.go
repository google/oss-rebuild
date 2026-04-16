// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestInferStrategy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		pkg            string
		version        string
		repoYAML       string
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
			wantCommitID: "initial",
			wantStrategyFn: func(commitHash string) rebuild.Strategy {
				return &GemBuild{
					Location: rebuild.Location{
						Repo: "https://github.com/test-org/test-gem",
						Ref:  commitHash,
					},
					RubyVersion: "3.3.11",
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
			wantErr: true,
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
			mux := rebuild.RegistryMux{}
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
