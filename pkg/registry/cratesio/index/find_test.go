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
			name: "distinguish versions sharing an index path across repos",
			repoYAMLs: []string{
				`commits:
  - id: current-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"new123","features":{},"yanked":false}
`,
				`commits:
  - id: snapshot-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123","features":{},"yanked":false}
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.0"},
				{Name: "serde", Version: "1.0.193"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  0,
			wantCommit:     "current-commit",
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
			name: "refine tail when coarse scan finds no drop",
			repoYAMLs: []string{
				`commits:
  - id: initial-commit
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"abc123","features":{},"yanked":false}
  - id: second-commit
    parent: initial-commit
  - id: third-commit
    parent: second-commit
`,
			},
			packages: []cargolock.Package{
				{Name: "serde", Version: "1.0.193"},
			},
			cratePublished: time.Now(),
			wantRepoIndex:  0,
			wantCommit:     "initial-commit",
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

func TestFindRegistryResolutionAtPackage(t *testing.T) {
	repo := mustCreateRepo(t, `commits:
  - id: initial
    files:
      de/pe/dependency: |
        {"name":"dependency","vers":"1.0.0"}
  - id: target
    parent: initial
    files:
      de/pe/dependency: |
        {"name":"dependency","vers":"1.0.0"}
        {"name":"dependency","vers":"2.0.0"}
  - id: later
    parent: target
    files:
      de/pe/dependency: |
        {"name":"dependency","vers":"1.0.0"}
        {"name":"dependency","vers":"2.0.0"}
        {"name":"dependency","vers":"3.0.0"}
`)
	target := cargolock.Package{Name: "dependency", Version: "2.0.0"}

	if _, err := FindRegistryResolution(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{target},
		time.Time{}.Add(-time.Second),
		nil,
	); err == nil {
		t.Fatal("FindRegistryResolution() succeeded before the target was indexed")
	}

	resolution, err := FindRegistryResolutionAtPackage(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{target},
		target,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.CommitHash != repo.Commits["target"] {
		t.Fatalf("FindRegistryResolutionAtPackage() = %s, want %s", resolution.CommitHash, repo.Commits["target"])
	}

	targetCommit, err := repo.CommitObject(repo.Commits["target"])
	if err != nil {
		t.Fatal(err)
	}
	laterCommit, err := repo.CommitObject(repo.Commits["later"])
	if err != nil {
		t.Fatal(err)
	}
	if !targetCommit.Committer.When.Equal(laterCommit.Committer.When) {
		t.Fatal("fixture commits must share a timestamp")
	}
	if _, err := FindRegistryResolutionAtPackage(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{{Name: "dependency", Version: "3.0.0"}},
		target,
		nil,
	); err == nil {
		t.Fatal("FindRegistryResolutionAtPackage() included a later commit with the same timestamp")
	}
	if _, err := FindRegistryResolutionAtPackage(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{
			{Name: "dependency", Version: "1.0.0"},
			{Name: "dependency", Version: "3.0.0"},
		},
		target,
		nil,
	); err == nil {
		t.Fatal("FindRegistryResolutionAtPackage() accepted an incomplete upper bound")
	}

	if _, err := FindRegistryResolutionAtPackage(
		[]*git.Repository{repo.Repository},
		[]cargolock.Package{target},
		cargolock.Package{Name: "dependency", Version: "4.0.0"},
		nil,
	); err != ErrTargetPackageNotFound {
		t.Fatalf("FindRegistryResolutionAtPackage() error = %v, want %v", err, ErrTargetPackageNotFound)
	}
}

func TestFindRegistryResolutionExcludesPathPackage(t *testing.T) {
	current := mustCreateRepo(t, `commits:
  - id: current
    files:
      pr/iv/private: |
        {"name":"private","vers":"2.0.0","deps":[],"cksum":"path","features":{},"yanked":false}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"dependency","features":{},"yanked":false}
`)
	snapshot := mustCreateRepo(t, `commits:
  - id: snapshot
    files:
      se/rd/serde: |
        {"name":"serde","vers":"1.0.193","deps":[],"cksum":"dependency","features":{},"yanked":false}
`)
	lockfile, err := cargolock.ParseLockfile(`version = 3

[[package]]
name = "example"
version = "1.0.0"

[[package]]
name = "private"
version = "2.0.0"

[[package]]
name = "serde"
version = "1.0.193"
source = "registry+https://github.com/rust-lang/crates.io-index"
`)
	if err != nil {
		t.Fatalf("ParseLockfile() error = %v", err)
	}

	if _, err := FindRegistryResolution(
		[]*git.Repository{current.Repository, snapshot.Repository},
		lockfile.Packages,
		time.Now(),
		nil,
	); err == nil {
		t.Fatal("FindRegistryResolution() with all packages succeeded")
	}

	filtered, err := FindRegistryResolution(
		[]*git.Repository{current.Repository, snapshot.Repository},
		cargolock.CratesIOPackages(lockfile.Packages),
		time.Now(),
		nil,
	)
	if err != nil {
		t.Fatalf("FindRegistryResolution() with crates.io packages error = %v", err)
	}
	if filtered.CommitHash != snapshot.Commits["snapshot"] {
		t.Errorf("filtered resolution = %v, want snapshot index", filtered.CommitHash)
	}
}

func mustCreateRepo(t *testing.T, history string) *gitxtest.Repository {
	t.Helper()
	repo, err := gitxtest.CreateRepoFromYAML(history, nil)
	if err != nil {
		t.Fatalf("CreateRepoFromYAML() error = %v", err)
	}
	return repo
}
