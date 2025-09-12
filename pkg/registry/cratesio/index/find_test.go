// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
)

func TestGetPackageFilePath(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		want string
	}{
		{"single char", "a", "1/a"},
		{"two chars", "ab", "2/ab"},
		{"three chars", "abc", "3/a/abc"},
		{"four chars", "serde", "se/rd/serde"},
		{"long package", "very-long-package", "ve/ry/very-long-package"},
		{"uppercase", "SERDE", "se/rd/serde"},
		{"with underscores", "test_crate", "te/st/test_crate"},
		{"with hyphens", "test-crate", "te/st/test-crate"},
		{"numbers", "log4rs", "lo/g4/log4rs"},
		{"mixed case", "CamelCase", "ca/me/camelcase"},
		{"very short mixed", "A", "1/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPackageFilePath(tt.pkg)
			if got != tt.want {
				t.Errorf("getPackageFilePath(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
	}
}

func TestFindRegistryResolution(t *testing.T) {
	tests := []struct {
		name           string
		repoYAML       string
		packages       []cargolock.Package
		cratePublished time.Time
		wantCommit     string // commit ID from test repo
		wantNil        bool
		wantErr        bool
	}{
		{
			name: "find packages in registry",
			repoYAML: `commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
  - id: serde-update
    parent: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
  - id: tokio-update
    parent: serde-update
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
        {"name":"tokio","vers":"1.35.1","deps":[],"cksum":"new456","features":{},"yanked":false}
`,
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
				{Name: "tokio", Version: "1.35.1"},
			},
			cratePublished: time.Now(),
			wantCommit:     "tokio-update",
		},
		{
			name: "packages not found",
			repoYAML: `commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
`,
			packages: []cargolock.Package{
				{Name: "nonexistent", Version: "1.0.0"},
			},
			cratePublished: time.Now(),
			wantNil:        true,
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := gitxtest.CreateRepoFromYAML(tt.repoYAML, nil)
			if err != nil {
				t.Fatalf("Failed to create test repo: %v", err)
			}
			got, err := FindRegistryResolution(repo.Repository, tt.packages, tt.cratePublished)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindRegistryResolution() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("FindRegistryResolution() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Errorf("FindRegistryResolution() = nil, want non-nil")
				return
			}
			if tt.wantCommit != "" {
				wantHash := repo.Commits[tt.wantCommit]
				if got.CommitHash != wantHash {
					t.Errorf("FindRegistryResolution().CommitHash = %v, want %v", got.CommitHash, wantHash)
				}
			}
		})
	}
}

func TestFindRegistryResolutionMultiRepo(t *testing.T) {
	tests := []struct {
		name           string
		repoYAMLs      []string
		packages       []cargolock.Package
		cratePublished time.Time
		wantRepoIndex  int    // which repo should contain the result
		wantCommit     string // commit ID from test repo
		wantNil        bool
	}{
		{
			name: "find in first repo and only repo",
			repoYAMLs: []string{
				// Current repo with recent packages
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
        {"name":"tokio","vers":"1.35.1","deps":[],"cksum":"new456","features":{},"yanked":false}
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
				{Name: "tokio", Version: "1.35.1"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  0,
			wantCommit:     "initial-commit",
		},
		{
			name: "find in first repo with no matches in second repo",
			repoYAMLs: []string{
				// Current repo with recent packages
				`commits:
  - id: add-serde
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
  - id: add-tokio
    parent: add-serde
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
        {"name":"tokio","vers":"1.35.1","deps":[],"cksum":"new456","features":{},"yanked":false}
`,
				// Snapshot repo with older packages
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
				{Name: "tokio", Version: "1.35.1"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  0,
			wantCommit:     "add-tokio",
		},
		{
			name: "find at repo boundary with non-zero matches in second repo",
			repoYAMLs: []string{
				// Current repo with recent packages
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
        {"name":"tokio","vers":"1.35.1","deps":[],"cksum":"new456","features":{},"yanked":false}
`,
				// Snapshot repo with older packages
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"def456","features":{},"yanked":false}
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
				{Name: "tokio", Version: "1.35.1"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  0,
			wantCommit:     "initial-commit",
		},
		{
			name: "find in second repo when first not found",
			repoYAMLs: []string{
				// Current repo without the packages
				`commits:
  - id: current-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.900","deps":[],"cksum":"newer123","features":{},"yanked":false}
`,
				// Snapshot repo with the packages
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
  - id: snapshot-commit
    parent: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  1,
			wantCommit:     "snapshot-commit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repos []*gitxtest.Repository
			var indices []*git.Repository
			for _, repoYAML := range tt.repoYAMLs {
				repo, err := gitxtest.CreateRepoFromYAML(repoYAML, nil)
				if err != nil {
					t.Fatalf("Failed to create test repo: %v", err)
				}
				repos = append(repos, repo)
				indices = append(indices, repo.Repository)
			}
			got, err := FindRegistryResolutionMultiRepo(indices, tt.packages, tt.cratePublished)
			if err != nil {
				t.Errorf("FindRegistryResolutionMultiRepo() error = %v", err)
				return
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("FindRegistryResolutionMultiRepo() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Errorf("FindRegistryResolutionMultiRepo() = nil, want non-nil")
				return
			}
			if tt.wantCommit != "" {
				wantHash := repos[tt.wantRepoIndex].Commits[tt.wantCommit]
				if got.CommitHash != wantHash {
					t.Errorf("FindRegistryResolutionMultiRepo().CommitHash = %v, want %v", got.CommitHash, wantHash)
				}
			}
		})
	}
}
