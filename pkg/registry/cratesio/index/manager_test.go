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
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
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
	must(gitxtest.CreateRepoFromYAML(yamlRepo, &gitxtest.RepositoryOptions{
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
	// Create manager with in-memory filesystem
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            memfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: time.Hour,
	})
	defer mgr.Close()
	// Create test fetcher to track calls
	testFetcher := &TestFetcher{fetchDelay: 100 * time.Millisecond}
	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}
	// Launch multiple goroutines requesting the same repo
	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)
	handles := make([]*RepositoryHandle, numGoroutines)
	// Pre-create the cached repository with our test fetcher and mark as initialized
	cached := &cachedRepository{
		key:     key,
		path:    mgr.getRepositoryPath(key),
		fetcher: testFetcher,
	}
	// Mark as initialized but not yet called
	cached.initOnce.Do(func() {
		// Call the test fetcher to track the call and create the repo
		repoFs := must(mgr.fs.Chroot(cached.path))
		cached.initErr = testFetcher.Fetch(ctx, repoFs)
		cached.lastUpdate.Store(time.Now().UnixMilli())
		cached.lastAccess.Store(time.Now().UnixMilli())
	})
	mgr.repositories.Store(key, cached)
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
	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
		if handles[i] != nil {
			handles[i].Close()
		}
	}
	// Verify only one fetch occurred
	fetches, _ := testFetcher.GetCounts()
	if fetches != 1 {
		t.Errorf("Expected exactly one fetch for concurrent requests, got %d", fetches)
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
	must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitxtest.RepositoryOptions{Storer: storer}))
	// Patch the currentIndexURL to point to our test repository
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
	key2 := RepositoryKey{Type: SnapshotIndex, Name: "2024-02-01"}
	h2 := must(mgr.GetRepository(ctx, key2))
	h2.Close()
	// Create third snapshot - this should trigger eviction of 2024-01-01 (LRU)
	key3 := RepositoryKey{Type: SnapshotIndex, Name: "2024-03-01"}
	h3 := must(mgr.GetRepository(ctx, key3))
	h3.Close()
	// Verify the correct snapshot was evicted
	if _, exists := mgr.repositories.Load(key1); exists {
		t.Errorf("2024-01-01 should have been evicted")
	}
	if _, exists := mgr.repositories.Load(key2); !exists {
		t.Errorf("2024-02-01 should exist")
	}
	if _, exists := mgr.repositories.Load(key3); !exists {
		t.Errorf("2024-03-01 should exist")
	}
	// Verify filesystem cleanup
	_, err := tempFS.Stat("snapshots/2024-01-01")
	if err == nil {
		t.Error("Evicted snapshot directory should be removed")
	}
}

// TestReaderWriterCoordination verifies updates wait for active readers
func TestReaderWriterCoordination(t *testing.T) {
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            memfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: 50 * time.Millisecond, // Short interval for testing
	})
	defer mgr.Close()
	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}
	// Create repo with tracking fetcher
	repoPath := mgr.getRepositoryPath(key)
	repoFs := must(mgr.fs.Chroot(repoPath))
	setupTestRepo(repoFs, "main")
	testFetcher := &TestFetcher{
		updateDelay: 50 * time.Millisecond,
	}
	cached := &cachedRepository{
		key:     key,
		path:    repoPath,
		fetcher: testFetcher,
	}
	// Mark as already initialized to prevent real fetcher usage
	cached.initOnce.Do(func() { cached.initErr = nil })
	// Set lastUpdate to ensure no update is needed initially
	cached.lastUpdate.Store(time.Now().UnixMilli())
	cached.lastAccess.Store(time.Now().UnixMilli())
	mgr.repositories.Store(key, cached)
	// Get initial handle but leave it open
	h1 := must(mgr.GetRepository(ctx, key))
	// Keep handle open to block updates
	defer h1.Close()
	// Wait until update interval passed
	time.Sleep(50 * time.Millisecond)
	// Configure and start update, giving the update time to try acquiring lock
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
	// Update should be blocked
	select {
	case <-updateCompleted:
		t.Fatal("Update completed while reader was active")
	case <-time.After(40 * time.Millisecond):
		// Expected - update is blocked
	}
	// Close the handle to unblock update
	h1.Close()
	// Update should now complete
	select {
	case <-updateCompleted:
		// Success
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Update did not complete after reader released")
	}
}

// TestCurrentIndexAutoUpdate verifies automatic updates based on interval using real CurrentIndex infrastructure
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
	repo := must(gitxtest.CreateRepoFromYAML(upstreamYaml, &gitxtest.RepositoryOptions{
		Storer:   filesystem.NewStorage(upstreamFs, cache.NewObjectLRUDefault()),
		Worktree: memfs.New(),
	}))
	// Patch the currentIndexURL to point to our test repository
	{
		originalCurrentURL := currentIndexURL
		currentIndexURL = "file://" + tempDir
		defer func() { currentIndexURL = originalCurrentURL }()
	}
	// Create manager with short update interval for speedy testing
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            memfs.New(),
		MaxSnapshots:          3,
		CurrentUpdateInterval: 50 * time.Millisecond, // Very short interval for fast testing
	})
	defer mgr.Close()
	ctx := context.Background()
	key := RepositoryKey{Type: CurrentIndex}
	// First access should trigger initial fetch
	beforeRepo := must(mgr.GetRepository(ctx, key))
	beforeHead := must(beforeRepo.CommitObject(must(beforeRepo.Head()).Hash()))
	// new-package should not exist yet
	fname := "ne/w/new-package"
	if _, err := beforeHead.File(fname); err == nil {
		t.Error("new-package file should not exist in git tree before upstream update")
	}
	beforeRepo.Close()
	// Add the new-package commit
	wt := must(repo.Worktree())
	f := must(wt.Filesystem.Create(fname))
	must(io.WriteString(f, `{"name":"new-package","vers":"1.0.0","deps":[],"cksum":"xyz789...","features":{},"yanked":false}`))
	must1(f.Close())
	must(wt.Add(fname))
	must(wt.Commit("commit", &git.CommitOptions{Author: &object.Signature{Name: "Place Holder"}}))
	// Wait for the update interval to pass
	time.Sleep(60 * time.Millisecond)
	// New access should trigger update since interval has passed
	afterRepo := must(mgr.GetRepository(ctx, key))
	defer afterRepo.Close()
	afterHead := must(afterRepo.CommitObject(must(afterRepo.Head()).Hash()))
	// new-package should now exist
	if _, err := afterHead.File(fname); err != nil {
		t.Errorf("new-package file should exist in git tree after upstream update, but got error: %v", err)
	}
}

// TestLoadExistingRepositories verifies loading pre-existing repos from filesystem
func TestLoadExistingRepositories(t *testing.T) {
	// Use temp directory for real filesystem operations
	tempDir := t.TempDir()
	fs := osfs.New(tempDir)
	// Pre-create some repositories
	currentPath := filepath.Join(tempDir, "current")
	must1(fs.MkdirAll(currentPath, 0755))
	setupTestRepo(osfs.New(currentPath), "main")
	snapshot1Path := filepath.Join(tempDir, "snapshots", "2024-01-01")
	must1(fs.MkdirAll(snapshot1Path, 0755))
	setupTestRepo(osfs.New(snapshot1Path), "snapshot-2024-01-01")
	snapshot2Path := filepath.Join(tempDir, "snapshots", "2024-02-01")
	must1(fs.MkdirAll(snapshot2Path, 0755))
	setupTestRepo(osfs.New(snapshot2Path), "snapshot-2024-02-01")
	// Create invalid directory that should be skipped
	invalidPath := filepath.Join(tempDir, "snapshots", "invalid")
	must1(fs.MkdirAll(invalidPath, 0755))
	// Load manager from filesystem
	mgr := must(NewIndexManagerFromFS(IndexManagerConfig{
		Filesystem:            fs,
		MaxSnapshots:          5,
		CurrentUpdateInterval: time.Hour,
	}))
	defer mgr.Close()
	// Verify repos were loaded
	currentKey := RepositoryKey{Type: CurrentIndex}
	if _, exists := mgr.repositories.Load(currentKey); !exists {
		t.Error("Current index should be loaded")
	}
	if _, exists := mgr.repositories.Load(RepositoryKey{Type: SnapshotIndex, Name: "2024-01-01"}); !exists {
		t.Error("Snapshot 2024-01-01 should be loaded")
	}
	if _, exists := mgr.repositories.Load(RepositoryKey{Type: SnapshotIndex, Name: "2024-02-01"}); !exists {
		t.Error("Snapshot 2024-02-01 should be loaded")
	}
	if _, exists := mgr.repositories.Load(RepositoryKey{Type: SnapshotIndex, Name: "invalid"}); exists {
		t.Error("Invalid directory should not be loaded")
	}
	// Verify loaded repos are usable
	ctx := context.Background()
	handle := must(mgr.GetRepository(ctx, currentKey))
	if handle == nil {
		t.Error("Handle should not be nil")
	}
	handle.Close()
}

// TestManagerCleanup verifies proper shutdown and resource cleanup
func TestManagerCleanup(t *testing.T) {
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            memfs.New(),
		MaxSnapshots:          2,
		CurrentUpdateInterval: time.Hour,
	})
	ctx := context.Background()
	// Create some repositories
	for i := range 3 {
		key := RepositoryKey{Type: SnapshotIndex, Name: fmt.Sprintf("2024-0%d-01", i+1)}
		// Create actual repository on disk
		repoPath := mgr.getRepositoryPath(key)
		repoFs := must(mgr.fs.Chroot(repoPath))
		setupTestRepo(repoFs, "main")
		cached := &cachedRepository{
			key:     key,
			path:    repoPath,
			fetcher: &TestFetcher{},
		}
		// Mark as already initialized to prevent real fetcher usage
		cached.initOnce.Do(func() { cached.initErr = nil })
		cached.lastUpdate.Store(time.Now().UnixMilli())
		cached.lastAccess.Store(time.Now().UnixMilli())
		mgr.repositories.Store(key, cached)
		h := must(mgr.GetRepository(ctx, key))
		h.Close()
	}
	// Close manager
	err := mgr.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}
	// Verify eviction channel is closed by trying to send
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when sending to closed channel")
		}
	}()
	mgr.evictCh <- evictRequest{}
}
