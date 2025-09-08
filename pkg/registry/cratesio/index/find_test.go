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
			got := EntryPath(tt.pkg)
			if got != tt.want {
				t.Errorf("getPackageFilePath(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
	}
}

func TestFindRegistryResolution(t *testing.T) {
	tests := []struct {
		name           string
		repoYAMLs      []string
		packages       []cargolock.Package
		cratePublished time.Time
		wantRepoIndex  int    // which repo should contain the result
		wantCommit     string // commit ID from test repo
		wantNil        bool
		wantErr        bool
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
		{
			name: "empty packages is an error",
			repoYAMLs: []string{
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
			packages:       []cargolock.Package{},
			cratePublished: time.Now(),
			wantErr:        true,
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
			got, err := FindRegistryResolution(indices, tt.packages, tt.cratePublished, nil)
			if err != nil || tt.wantErr {
				if err != nil != tt.wantErr {
					t.Errorf("FindRegistryResolution() want error = %t, error = %v", tt.wantErr, err)
				}
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
				wantHash := repos[tt.wantRepoIndex].Commits[tt.wantCommit]
				if got.CommitHash != wantHash {
					t.Errorf("FindRegistryResolution().CommitHash = %v, want %v", got.CommitHash, wantHash)
				}
			}
		})
	}
}
