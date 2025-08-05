// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"io"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

var objCache = cache.NewObjectLRUDefault()

func TestListAvailableSnapshots(t *testing.T) {
	tempDir := t.TempDir()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial archive commit"
    files:
      README.md: "Crates.io Index Archive"
  - id: snapshot-jan
    parent: initial
    branch: snapshot-2025-01-01
    message: "Snapshot for 2025-01-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123...","features":{},"yanked":false}
  - id: snapshot-feb
    parent: initial
    branch: snapshot-2025-02-15
    message: "Snapshot for 2025-02-15"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.1","deps":[],"cksum":"def456...","features":{},"yanked":false}
  - id: snapshot-mar
    parent: initial
    branch: snapshot-2025-03-31
    message: "Snapshot for 2025-03-31"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"abc123...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.1","deps":[],"cksum":"def456...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.2","deps":[],"cksum":"ghi789...","features":{},"yanked":false}
  - id: feature-branch
    parent: initial
    branch: feature/new-format
    message: "Feature branch (should be ignored)"
    files:
      experimental.txt: "This is not a snapshot"
`
	osFS := osfs.New(tempDir)
	must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitx.RepositoryOptions{
		Storer: filesystem.NewStorage(osFS, objCache),
	}))
	{
		originalArchiveURL := archiveIndexURL
		archiveIndexURL = "file://" + tempDir
		defer func() { archiveIndexURL = originalArchiveURL }()
	}
	ctx := context.Background()
	snapshots, err := ListAvailableSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListAvailableSnapshots() error = %v", err)
	}
	expectedSnapshots := []string{"2025-01-01", "2025-02-15", "2025-03-31"}
	if diff := cmp.Diff(expectedSnapshots, snapshots); diff != "" {
		t.Errorf("Snapshots mismatch (-want +got):\n%s", diff)
	}
}

func TestCurrentIndexFetcher(t *testing.T) {
	tempDir := t.TempDir()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates", "api": "https://crates.io"}
  - id: add-crates
    parent: initial
    branch: master
    message: "Add various crates"
    files:
      # 1-char crate
      1/a: |
        {"name":"a","vers":"0.1.0","deps":[],"cksum":"aaa...","features":{},"yanked":false}
      # 2-char crate
      2/ab: |
        {"name":"ab","vers":"0.1.0","deps":[],"cksum":"bbb...","features":{},"yanked":false}
      # 3-char crate
      3/a/abc: |
        {"name":"abc","vers":"0.1.0","deps":[],"cksum":"ccc...","features":{},"yanked":false}
      # 4+ char crates
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"ddd...","features":{},"yanked":false}
      to/ki/tokio: |
        {"name":"tokio","vers":"1.0.0","deps":[],"cksum":"eee...","features":{},"yanked":false}
`
	osFS := osfs.New(tempDir)
	must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitx.RepositoryOptions{
		Storer: filesystem.NewStorage(osFS, cache.NewObjectLRUDefault()),
	}))
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}
	ctx := context.Background()
	mfs := memfs.New()
	fetcher := &CurrentIndexFetcher{}
	err := fetcher.Fetch(ctx, mfs)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	storer := filesystem.NewStorage(mfs, nil)
	repo := must(git.Open(storer, nil))
	ref := must(repo.Reference(plumbing.Master, false))
	if ref.Hash().IsZero() {
		t.Error("Master branch points to zero hash")
	}
	refs := must(repo.References())
	var refCount int
	must1(refs.ForEach(func(*plumbing.Reference) error {
		refCount++
		return nil
	}))
	if refCount == 0 {
		t.Error("Expected some references in cloned repository")
	}
}

func TestSnapshotIndexFetcher(t *testing.T) {
	tempDir := t.TempDir()
	yamlRepo := `
commits:
  - id: base
    branch: master
    message: "Base commit"
    files:
      README.md: "Archive base"
  # First snapshot branch - completely independent history
  - id: snap1-initial
    branch: snapshot-2025-01-01
    message: "January snapshot"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"jan1...","features":{},"yanked":false}
  - id: snap1-update
    parent: snap1-initial
    branch: snapshot-2025-01-01
    message: "January update"
    files:
      to/ki/tokio: |
        {"name":"tokio","vers":"0.9.0","deps":[],"cksum":"jan2...","features":{},"yanked":false}
  # Second snapshot branch - different history
  - id: snap2-initial
    branch: snapshot-2025-02-01
    message: "February snapshot"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"feb1...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.1","deps":[],"cksum":"feb2...","features":{},"yanked":false}
  # Third snapshot branch - yet another independent history
  - id: snap3-initial
    branch: snapshot-2025-03-01
    message: "March snapshot"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      se/rd/serde: |
        {"name":"serde","vers":"1.0.0","deps":[],"cksum":"mar1...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.1","deps":[],"cksum":"mar2...","features":{},"yanked":false}
        {"name":"serde","vers":"1.0.2","deps":[],"cksum":"mar3...","features":{},"yanked":false}
  - id: snap3-extra
    parent: snap3-initial
    branch: snapshot-2025-03-01
    message: "March extra commit"
    files:
      ra/nd/rand: |
        {"name":"rand","vers":"0.8.0","deps":[],"cksum":"mar4...","features":{},"yanked":false}
`
	osFS := osfs.New(tempDir)
	testRepo := must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitx.RepositoryOptions{
		Storer: filesystem.NewStorage(osFS, cache.NewObjectLRUDefault()),
	}))
	{
		originalArchiveURL := archiveIndexURL
		archiveIndexURL = "file://" + tempDir
		defer func() { archiveIndexURL = originalArchiveURL }()
	}
	tests := []struct {
		name           string
		date           string
		wantCommits    int
		wantBranchName string
	}{
		{
			name:           "january_snapshot",
			date:           "2025-01-01",
			wantCommits:    2, // snap1-initial and snap1-update
			wantBranchName: "snapshot-2025-01-01",
		},
		{
			name:           "february_snapshot",
			date:           "2025-02-01",
			wantCommits:    1, // only snap2-initial
			wantBranchName: "snapshot-2025-02-01",
		},
		{
			name:           "march_snapshot",
			date:           "2025-03-01",
			wantCommits:    2, // snap3-initial and snap3-extra
			wantBranchName: "snapshot-2025-03-01",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mfs := memfs.New()
			fetcher := &SnapshotIndexFetcher{Date: tt.date}
			err := fetcher.Fetch(ctx, mfs)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			storer := filesystem.NewStorage(mfs, nil)
			repo := must(git.Open(storer, nil))
			expectedBranch := plumbing.NewBranchReferenceName(tt.wantBranchName)
			branchRef := must(repo.Reference(expectedBranch, false))
			if branchRef.Hash().IsZero() {
				t.Errorf("Branch %s points to zero hash", tt.wantBranchName)
			}
			origBranchRef := must(testRepo.Reference(expectedBranch, false))
			if diff := cmp.Diff(origBranchRef.Hash().String(), branchRef.Hash().String()); diff != "" {
				t.Errorf("Branch HEAD mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSnapshotIndexFetcher_Update(t *testing.T) {
	fetcher := &SnapshotIndexFetcher{Date: "2025-01-01"}
	ctx := context.Background()
	mfs := memfs.New()
	err := fetcher.Update(ctx, mfs)
	if err != nil {
		t.Errorf("Update() should always return nil for snapshots, got: %v", err)
	}
}

func TestCurrentIndexFetcher_Update(t *testing.T) {
	ctx := context.Background()
	fetcher := &CurrentIndexFetcher{}
	tempDir := t.TempDir()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	upstream := must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitx.RepositoryOptions{
		Storer: filesystem.NewStorage(osfs.New(tempDir), cache.NewObjectLRUDefault()),
	}))
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}
	cloneFS := memfs.New()
	must1(fetcher.Fetch(ctx, cloneFS))
	// Add new file to the upstream
	wt := must(upstream.Worktree())
	fname := "se/rd/serde"
	f := must(wt.Filesystem.Create(fname))
	must(io.WriteString(f, `{"name":"serde","vers":"1.0.0","deps":[],"cksum":"mar1...","features":{},"yanked":false}`))
	must1(f.Close())
	must(wt.Add(fname))
	newUpstreamCommit := must(wt.Commit("add serde", &git.CommitOptions{Author: &object.Signature{Name: "Place Holder"}}))
	// Update and assert we pull down that update
	must1(fetcher.Update(ctx, cloneFS))
	clone := must(git.Open(filesystem.NewStorage(cloneFS, cache.NewObjectLRUDefault()), memfs.New()))
	newCloneCommit := must(clone.CommitObject(newUpstreamCommit))
	tree := must(newCloneCommit.Tree())
	if _, err := tree.File(fname); err != nil {
		t.Errorf("Update failed to add expected file: %s", fname)
	}
}

func must[T any](t T, err error) T {
	must1(err)
	return t
}

func must1(err error) {
	if err != nil {
		panic(err)
	}
}
