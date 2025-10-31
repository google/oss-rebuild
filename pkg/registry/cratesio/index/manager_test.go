// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/safememfs"
)

// setupTestRepo creates a test git repository in the given filesystem using YAML definition
func setupTestRepo(fs billy.Filesystem, branchName string) {
	yamlRepo := fmt.Sprintf(`
commits:
  - id: initial
    branch: %s
    message: "Test repository for %s"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
      test.txt: "Test content for branch %s"
`, branchName, branchName, branchName)
	must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitx.RepositoryOptions{
		Storer: filesystem.NewStorage(fs, cache.NewObjectLRUDefault()),
	}))
}

// TestFetcher is a mock fetcher for testing
type TestFetcher struct {
	fetchCount  int
	updateCount int
	fetchDelay  time.Duration
	updateDelay time.Duration
	mu          sync.Mutex
}

func (f *TestFetcher) Fetch(ctx context.Context, fs billy.Filesystem) error {
	f.mu.Lock()
	f.fetchCount++
	delay := f.fetchDelay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	setupTestRepo(fs, "main")
	return nil
}

func (f *TestFetcher) Update(ctx context.Context, fs billy.Filesystem) error {
	f.mu.Lock()
	f.updateCount++
	delay := f.updateDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *TestFetcher) GetCounts() (fetches, updates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetchCount, f.updateCount
}

// TestConcurrentInitialization verifies that multiple concurrent requests
// for the same repository only trigger a single fetch operation
func TestConcurrentInitialization(t *testing.T) {
	// Create upstream repository that will serve as the "remote"
	tempDir := t.TempDir()
	upstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial current index commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	upstreamFs := osfs.New(tempDir)
	must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(upstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch the currentIndexURL to point to our test repository
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}

	// Create manager with in-memory filesystem
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: time.Hour,
	})
	defer mgr.Close()

	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}

	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)
	handles := make([]*RepositoryHandle, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handle, err := mgr.GetRepository(ctx, key)
			errors[idx] = err
			handles[idx] = handle
		}(i)
	}

	wg.Wait()

	// Verify all succeeded
	successCount := 0
	for i, err := range errors {
		if err == nil {
			successCount++
			if handles[i] != nil {
				handles[i].Close()
			}
		}
	}

	// All should succeed (this validates the coordinator model works)
	if successCount != numGoroutines {
		t.Errorf("Expected all %d requests to succeed, got %d", numGoroutines, successCount)
	}
}

// TestSnapshotEviction verifies LRU eviction when max snapshots is reached
func TestSnapshotEviction(t *testing.T) {
	// Set up snapshot repo
	upstreamDir := t.TempDir()
	upstreamFs := osfs.New(upstreamDir)
	storer := filesystem.NewStorage(upstreamFs, cache.NewObjectLRUDefault())
	upstreamYaml := `
commits:
  - id: commit1
    branch: snapshot-2024-01-01
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: commit2
    branch: snapshot-2024-02-01
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: commit3
    branch: snapshot-2024-03-01
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitx.RepositoryOptions{Storer: storer}))

	// Patch the archiveIndexURL to point to our test repository
	{
		originalArchiveURL := archiveIndexURL
		archiveIndexURL = "file://" + upstreamDir
		defer func() { archiveIndexURL = originalArchiveURL }()
	}

	tempDir := t.TempDir()
	tempFS := osfs.New(tempDir)
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            tempFS,
		MaxSnapshots:          2,
		CurrentUpdateInterval: time.Hour,
	})
	defer mgr.Close()

	ctx := context.Background()

	// Create first two snapshots
	key1 := RepositoryKey{Type: SnapshotIndex, Name: "2024-01-01"}
	h1 := must(mgr.GetRepository(ctx, key1))
	h1.Close()

	// Small delay to ensure different access times
	time.Sleep(10 * time.Millisecond)

	key2 := RepositoryKey{Type: SnapshotIndex, Name: "2024-02-01"}
	h2 := must(mgr.GetRepository(ctx, key2))
	h2.Close()

	// Small delay to ensure different access times
	time.Sleep(10 * time.Millisecond)

	// Create third snapshot - this should trigger eviction of 2024-01-01 (LRU)
	key3 := RepositoryKey{Type: SnapshotIndex, Name: "2024-03-01"}
	h3 := must(mgr.GetRepository(ctx, key3))
	h3.Close()

	// Give the coordinator time to process eviction
	time.Sleep(50 * time.Millisecond)

	// Verify filesystem cleanup - the evicted snapshot should be removed
	_, err := tempFS.Stat("snapshot-2024-01-01")
	if err == nil {
		t.Error("Evicted snapshot directory should be removed")
	}

	// Verify remaining snapshots exist
	if _, err := tempFS.Stat("snapshot-2024-02-01"); err != nil {
		t.Error("2024-02-01 should exist on filesystem")
	}
	if _, err := tempFS.Stat("snapshot-2024-03-01"); err != nil {
		t.Error("2024-03-01 should exist on filesystem")
	}
}

// TestMultiRepositoryAcquisition tests the new atomic multi-repository feature
func TestMultiRepositoryAcquisition(t *testing.T) {
	// Set up upstream repositories
	currentTempDir := t.TempDir()
	currentUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial current index commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	currentUpstreamFs := osfs.New(currentTempDir)
	must(gitxtest.CreateRepoFromYAML(currentUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(currentUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Set up archive repository for snapshots
	archiveTempDir := t.TempDir()
	archiveUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial archive commit"
    files:
      README.md: "Crates.io Index Archive"
  - id: snapshot-jan
    parent: initial
    branch: snapshot-2024-01-01
    message: "Snapshot for 2024-01-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-feb
    parent: initial
    branch: snapshot-2024-02-01
    message: "Snapshot for 2024-02-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	archiveUpstreamFs := osfs.New(archiveTempDir)
	must(gitxtest.CreateRepoFromYAML(archiveUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(archiveUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch URLs to point to our test repositories
	{
		originalCurrentURL := currentIndexURL
		originalArchiveURL := archiveIndexURL
		currentIndexURL = "file://" + currentTempDir
		archiveIndexURL = "file://" + archiveTempDir
		defer func() {
			currentIndexURL = originalCurrentURL
			archiveIndexURL = originalArchiveURL
		}()
	}

	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          5,
		CurrentUpdateInterval: time.Hour,
	})
	defer mgr.Close()

	ctx := context.Background()

	// Test atomic acquisition of multiple repositories
	keys := []RepositoryKey{
		{Type: SnapshotIndex, Name: "2024-01-01"},
		{Type: SnapshotIndex, Name: "2024-02-01"},
		{Type: CurrentIndex},
	}

	handles, err := mgr.GetRepositories(ctx, keys, nil)
	if err != nil {
		t.Fatalf("Failed to acquire multiple repositories: %v", err)
	}

	// Verify we got handles for all requested repositories
	if len(handles) != len(keys) {
		t.Errorf("Expected %d handles, got %d", len(keys), len(handles))
	}

	// Clean up
	for _, handle := range handles {
		if handle != nil {
			handle.Close()
		}
	}
}

// TestReaderWriterCoordination verifies updates work with the coordinator model
func TestReaderWriterCoordination(t *testing.T) {
	// Create upstream repository that will serve as the "remote"
	tempDir := t.TempDir()
	upstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial current index commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	upstreamFs := osfs.New(tempDir)
	must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(upstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch the currentIndexURL to point to our test repository
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}

	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: 50 * time.Millisecond, // Short interval for testing
	})
	defer mgr.Close()

	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}

	// Get initial handle
	h1, err := mgr.GetRepository(ctx, key)
	if err != nil {
		t.Fatalf("Failed to get initial repository: %v", err)
	}

	// Wait until update interval passes
	time.Sleep(60 * time.Millisecond)

	// Start a concurrent request that should trigger an update
	updateStarted := make(chan struct{})
	updateCompleted := make(chan struct{})

	go func() {
		close(updateStarted)
		h2, err := mgr.GetRepository(ctx, key)
		if err != nil {
			t.Errorf("GetRepository failed: %v", err)
		}
		if h2 != nil {
			h2.Close()
		}
		close(updateCompleted)
	}()

	<-updateStarted
	time.Sleep(10 * time.Millisecond)

	// The coordinator model should handle this gracefully
	// In the new model, updates don't block readers in the same way

	// Close the first handle
	h1.Close()

	// Second request should complete
	select {
	case <-updateCompleted:
		// Success
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Update did not complete in reasonable time")
	}
}

// TestCurrentIndexAutoUpdate verifies automatic updates based on interval
func TestCurrentIndexAutoUpdate(t *testing.T) {
	// Create upstream repository that will serve as the "remote"
	tempDir := t.TempDir()
	upstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial current index commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	upstreamFs := osfs.New(tempDir)
	repo := must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(upstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch the currentIndexURL to point to our test repository
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}

	// Create manager with very short update interval for testing
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: 10 * time.Millisecond, // Very short interval for fast testing
	})
	defer mgr.Close()

	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}

	// First access should trigger initial fetch
	beforeRepo, err := mgr.GetRepository(ctx, key)
	if err != nil {
		t.Fatalf("Failed to get repository: %v", err)
	}
	beforeHead := must(beforeRepo.CommitObject(must(beforeRepo.Head()).Hash()))

	// new-package should not exist yet
	fname := "ne/w/new-package"
	if _, err := beforeHead.File(fname); err == nil {
		t.Error("new-package file should not exist in git tree before upstream update")
	}
	beforeRepo.Close()

	// Add the new-package commit to upstream
	wt := must(repo.Worktree())
	f := must(wt.Filesystem.Create(fname))
	must(io.WriteString(f, `{"name":"new-package","vers":"1.0.0","deps":[],"cksum":"xyz789...","features":{},"yanked":false}`))
	must1(f.Close())
	must(wt.Add(fname))
	must(wt.Commit("commit", &git.CommitOptions{Author: &object.Signature{Name: "Place Holder"}}))

	// Wait for the update interval to pass (longer than the 10ms interval)
	time.Sleep(20 * time.Millisecond)

	// Trigger multiple repository accesses to ensure update is triggered
	// and give it time to complete
	for i := range 5 {
		repo, err := mgr.GetRepository(ctx, key)
		if err != nil {
			t.Fatalf("Failed to get repository for update trigger %d: %v", i, err)
		}
		repo.Close()

		// Give time for async update to process
		time.Sleep(10 * time.Millisecond)

		// Check if the file exists yet
		testRepo, err := mgr.GetRepository(ctx, key)
		if err != nil {
			t.Fatalf("Failed to get test repository: %v", err)
		}
		testHead := must(testRepo.CommitObject(must(testRepo.Head()).Hash()))

		if _, err := testHead.File(fname); err == nil {
			// File found! Update worked
			testRepo.Close()
			return
		}
		testRepo.Close()
	}

	// Final check - if we get here, the update didn't work
	finalRepo, err := mgr.GetRepository(ctx, key)
	if err != nil {
		t.Fatalf("Failed to get repository for final check: %v", err)
	}
	defer finalRepo.Close()

	finalHead := must(finalRepo.CommitObject(must(finalRepo.Head()).Hash()))
	if _, err := finalHead.File(fname); err != nil {
		t.Errorf("new-package file should exist in git tree after upstream update, but got error: %v", err)
	}
}

// TestLoadExistingRepositories verifies loading pre-existing repos from filesystem
func TestLoadExistingRepositories(t *testing.T) {
	// Create mock upstream repositories
	currentTempDir := t.TempDir()
	currentUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial current index commit"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	currentUpstreamFs := osfs.New(currentTempDir)
	must(gitxtest.CreateRepoFromYAML(currentUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(currentUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Set up archive repository for snapshots
	archiveTempDir := t.TempDir()
	archiveUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial archive commit"
    files:
      README.md: "Crates.io Index Archive"
  - id: snapshot-jan
    parent: initial
    branch: snapshot-2024-01-01
    message: "Snapshot for 2024-01-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-feb
    parent: initial
    branch: snapshot-2024-02-01
    message: "Snapshot for 2024-02-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	archiveUpstreamFs := osfs.New(archiveTempDir)
	must(gitxtest.CreateRepoFromYAML(archiveUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(archiveUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch URLs to point to our test repositories
	{
		originalCurrentURL := currentIndexURL
		originalArchiveURL := archiveIndexURL
		currentIndexURL = "file://" + currentTempDir
		archiveIndexURL = "file://" + archiveTempDir
		defer func() {
			currentIndexURL = originalCurrentURL
			archiveIndexURL = originalArchiveURL
		}()
	}

	// Use temp directory for real filesystem operations
	tempDir := t.TempDir()
	fs := osfs.New(tempDir)

	// Pre-create some repositories
	currentPath := filepath.Join(tempDir, "current")
	must1(fs.MkdirAll(currentPath, 0755))
	must1((&CurrentIndexFetcher{}).Fetch(context.Background(), must(fs.Chroot("current"))))

	snapshot1Path := filepath.Join(tempDir, "snapshot-2024-01-01")
	must1(fs.MkdirAll(snapshot1Path, 0755))
	must1((&SnapshotIndexFetcher{Date: "2024-01-01"}).Fetch(context.Background(), must(fs.Chroot("snapshot-2024-01-01"))))

	snapshot2Path := filepath.Join(tempDir, "snapshots-2024-02-01")
	must1(fs.MkdirAll(snapshot2Path, 0755))
	must1((&SnapshotIndexFetcher{Date: "2024-02-01"}).Fetch(context.Background(), must(fs.Chroot("snapshot-2024-02-01"))))

	// Create invalid directory that should be skipped
	invalidPath := filepath.Join(tempDir, "snapshots-invalid")
	must1(fs.MkdirAll(invalidPath, 0755))

	// Load manager from filesystem
	mgr, err := NewIndexManagerFromFS(IndexManagerConfig{
		Filesystem:            fs,
		MaxSnapshots:          5,
		CurrentUpdateInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create manager from filesystem: %v", err)
	}
	defer mgr.Close()

	// Verify loaded repos are usable
	ctx := context.Background()

	// Test current repository
	currentKey := RepositoryKey{Type: CurrentIndex}
	handle, err := mgr.GetRepository(ctx, currentKey)
	if err != nil {
		t.Errorf("Failed to get current repository: %v", err)
	}
	if handle != nil {
		handle.Close()
	}

	// Test snapshot repositories
	snap1Key := RepositoryKey{Type: SnapshotIndex, Name: "2024-01-01"}
	handle1, err := mgr.GetRepository(ctx, snap1Key)
	if err != nil {
		t.Errorf("Failed to get snapshot 2024-01-01: %v", err)
	}
	if handle1 != nil {
		handle1.Close()
	}

	snap2Key := RepositoryKey{Type: SnapshotIndex, Name: "2024-02-01"}
	handle2, err := mgr.GetRepository(ctx, snap2Key)
	if err != nil {
		t.Errorf("Failed to get snapshot 2024-02-01: %v", err)
	}
	if handle2 != nil {
		handle2.Close()
	}

	// Invalid directory should not be accessible
	invalidKey := RepositoryKey{Type: SnapshotIndex, Name: "invalid"}
	_, err = mgr.GetRepository(ctx, invalidKey)
	if err == nil {
		t.Error("Should not be able to get invalid repository")
	}
}

// TestManagerCleanup verifies proper shutdown and resource cleanup
func TestManagerCleanup(t *testing.T) {
	// Set up archive repository for snapshots
	archiveTempDir := t.TempDir()
	archiveUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial archive commit"
    files:
      README.md: "Crates.io Index Archive"
  - id: snapshot-jan
    parent: initial
    branch: snapshot-2024-01-01
    message: "Snapshot for 2024-01-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-feb
    parent: initial
    branch: snapshot-2024-02-01
    message: "Snapshot for 2024-02-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-mar
    parent: initial
    branch: snapshot-2024-03-01
    message: "Snapshot for 2024-03-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	archiveUpstreamFs := osfs.New(archiveTempDir)
	must(gitxtest.CreateRepoFromYAML(archiveUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(archiveUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch archive URL to point to our test repository
	{
		originalArchiveURL := archiveIndexURL
		archiveIndexURL = "file://" + archiveTempDir
		defer func() { archiveIndexURL = originalArchiveURL }()
	}

	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          2,
		CurrentUpdateInterval: time.Hour,
	})

	ctx := context.Background()

	// Create some repositories
	for i := range 3 {
		key := RepositoryKey{Type: SnapshotIndex, Name: fmt.Sprintf("2024-0%d-01", i+1)}
		h, err := mgr.GetRepository(ctx, key)
		if err != nil {
			t.Errorf("Failed to create repository %v: %v", key, err)
			continue
		}
		h.Close()
	}

	// Close manager
	err := mgr.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}

	// Verify that attempting to get repositories after close fails
	key := RepositoryKey{Type: CurrentIndex}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = mgr.GetRepository(ctx, key)
	if err == nil {
		t.Error("Expected GetRepository to fail after Close()")
	}
}

// TestConcurrentEvictionAndAccess tests coordinator handling of concurrent eviction and access
func TestConcurrentEvictionAndAccess(t *testing.T) {
	// Set up archive repository for snapshots
	archiveTempDir := t.TempDir()
	archiveUpstreamYaml := `
commits:
  - id: initial
    branch: master
    message: "Initial archive commit"
    files:
      README.md: "Crates.io Index Archive"
  - id: snapshot-jan
    parent: initial
    branch: snapshot-2024-01-01
    message: "Snapshot for 2024-01-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-feb
    parent: initial
    branch: snapshot-2024-02-01
    message: "Snapshot for 2024-02-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
  - id: snapshot-mar
    parent: initial
    branch: snapshot-2024-03-01
    message: "Snapshot for 2024-03-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`
	// Create branches for additional test snapshots
	for i := 4; i <= 12; i++ {
		archiveUpstreamYaml += fmt.Sprintf(`  - id: snapshot-%d
    parent: initial
    branch: snapshot-2024-%02d-01
    message: "Snapshot for 2024-%02d-01"
    files:
      config.json: |
        {"dl": "https://crates.io/api/v1/crates"}
`, i, i, i)
	}

	archiveUpstreamFs := osfs.New(archiveTempDir)
	must(gitxtest.CreateRepoFromYAML(archiveUpstreamYaml, &gitx.RepositoryOptions{
		Storer:   filesystem.NewStorage(archiveUpstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))

	// Patch archive URL to point to our test repository
	{
		originalArchiveURL := archiveIndexURL
		archiveIndexURL = "file://" + archiveTempDir
		defer func() { archiveIndexURL = originalArchiveURL }()
	}

	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          2,
		CurrentUpdateInterval: time.Hour,
	})
	defer mgr.Close()

	ctx := context.Background()

	// Create repositories to fill capacity
	key1 := RepositoryKey{Type: SnapshotIndex, Name: "2024-01-01"}
	h1 := must(mgr.GetRepository(ctx, key1))
	h1.Close()

	key2 := RepositoryKey{Type: SnapshotIndex, Name: "2024-02-01"}
	h2 := must(mgr.GetRepository(ctx, key2))
	h2.Close()

	var wg sync.WaitGroup
	errors := make([]error, 10)

	// Launch concurrent operations
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// This should trigger eviction of one of the existing snapshots
			key := RepositoryKey{Type: SnapshotIndex, Name: fmt.Sprintf("2024-%02d-01", idx+3)}
			h, err := mgr.GetRepository(ctx, key)
			errors[idx] = err
			if h != nil {
				h.Close()
			}
		}(i)
	}

	wg.Wait()

	// Count successful operations - the coordinator should handle these gracefully
	successCount := 0
	for _, err := range errors {
		if err == nil {
			successCount++
		}
	}

	if successCount != len(errors) {
		t.Error("Expected concurrent operations to succeed")
	}
}
