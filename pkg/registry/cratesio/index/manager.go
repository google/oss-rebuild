// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/pkg/errors"
)

// IndexType represents the type of repository
type IndexType int

const (
	CurrentIndex IndexType = iota
	SnapshotIndex
)

// RepositoryKey uniquely identifies a repository
type RepositoryKey struct {
	Type IndexType
	Name string // date string for snapshots, empty for current
}

// cachedRepository represents a repository on disk with usage tracking
type cachedRepository struct {
	key        RepositoryKey
	path       string
	lastAccess atomic.Int64 // Unix timestamp
	lastUpdate atomic.Int64 // Unix timestamp
	fetcher    Fetcher
	// readersMu protects readers from concurrent access/updates
	readersMu sync.RWMutex
	// initOnce ensures initialization happens only once
	initOnce sync.Once
	initErr  error
}

// initialize ensures the repository is fetched and ready, using sync.Once for coalescing
func (c *cachedRepository) initialize(ctx context.Context, m *IndexManager) error {
	c.initOnce.Do(func() {
		c.initErr = c.doInitialize(ctx, m)
	})
	return c.initErr
}

// doInitialize performs the actual initialization work
func (c *cachedRepository) doInitialize(ctx context.Context, m *IndexManager) error {
	c.lastAccess.Store(time.Now().UnixMilli())
	// Check if we need to evict a snapshot first
	if c.key.Type == SnapshotIndex {
		if err := m.maybeEvictSnapshot(ctx); err != nil {
			return err
		}
	}
	switch c.key.Type {
	case CurrentIndex:
		c.fetcher = &CurrentIndexFetcher{}
	case SnapshotIndex:
		c.fetcher = &SnapshotIndexFetcher{Date: c.key.Name}
	default:
		return errors.Errorf("unknown repository type: %v", c.key.Type)
	}
	repoFs, err := m.fs.Chroot(c.path)
	if err != nil {
		return errors.Wrap(err, "failed to create repo directory")
	}
	fetchErr := c.fetcher.Fetch(ctx, repoFs)
	if fetchErr != nil {
		m.fs.Remove(c.path)
		return fetchErr
	}
	c.lastUpdate.Store(time.Now().UnixMilli())
	return nil
}

// IndexManager manages multiple repository versions with caching and eviction
type IndexManager struct {
	fs                    billy.Filesystem
	maxSnapshots          int
	currentUpdateInterval time.Duration
	repositories          *syncx.Map[RepositoryKey, *cachedRepository]
	// Channel to serialize eviction operations
	evictCh chan evictRequest
	wg      sync.WaitGroup
}

type evictRequest struct {
	key    RepositoryKey
	result chan error
}

// IndexManagerConfig configures the IndexManager
type IndexManagerConfig struct {
	Filesystem            billy.Filesystem
	MaxSnapshots          int
	CurrentUpdateInterval time.Duration
}

// NewIndexManager creates a new index manager
func NewIndexManager(cfg IndexManagerConfig) *IndexManager {
	m := &IndexManager{
		fs:                    cfg.Filesystem,
		maxSnapshots:          cfg.MaxSnapshots,
		currentUpdateInterval: cfg.CurrentUpdateInterval,
		repositories:          &syncx.Map[RepositoryKey, *cachedRepository]{},
		evictCh:               make(chan evictRequest),
	}
	// Start eviction worker
	m.wg.Add(1)
	go m.evictionWorker()
	return m
}

// NewIndexManagerFromFS creates a new index manager and loads any existing
// repositories from the provided filesystem. This is intended for scenarios
// where the filesystem is pre-populated and not concurrently modified.
func NewIndexManagerFromFS(cfg IndexManagerConfig) (*IndexManager, error) {
	m := NewIndexManager(cfg)
	// Attempt to load the current index if it exists.
	_ = m.loadExistingRepo(RepositoryKey{Type: CurrentIndex})
	// Attempt to load existing snapshots.
	snapshotBase := "snapshots"
	entries, err := m.fs.ReadDir(snapshotBase)
	if err != nil {
		// If the snapshots directory doesn't exist, that's fine.
		if os.IsNotExist(err) {
			return m, nil
		}
		// Any other error is unexpected.
		return nil, errors.Wrapf(err, "failed to read snapshots directory at %s", snapshotBase)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			name := entry.Name()
			_ = m.loadExistingRepo(RepositoryKey{Type: SnapshotIndex, Name: name})
		}
	}
	return m, nil
}

// loadExistingRepo checks a path for a valid git repository and adds it to the manager.
func (m *IndexManager) loadExistingRepo(key RepositoryKey) error {
	path := m.getRepositoryPath(key)
	repoFs, err := m.fs.Chroot(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil // Path doesn't exist, skip.
	}
	storer := filesystem.NewStorage(repoFs, cache.NewObjectLRUDefault())
	if _, err := git.Open(storer, nil); err != nil {
		return nil // Not a valid repo, skip.
	}
	cached := &cachedRepository{key: key, path: path}
	switch key.Type {
	case CurrentIndex:
		cached.fetcher = &CurrentIndexFetcher{}
	case SnapshotIndex:
		cached.fetcher = &SnapshotIndexFetcher{Date: key.Name}
	}
	f, err := m.fs.Stat(path)
	if err != nil {
		return nil // Unable to stat, skip.
	}
	cached.lastUpdate.Store(f.ModTime().UnixMilli())
	// Mark the repository as already initialized.
	cached.initOnce.Do(func() { cached.initErr = nil })
	m.repositories.Store(key, cached)
	return nil
}

// Close shuts down the index manager
func (m *IndexManager) Close() error {
	close(m.evictCh)
	m.wg.Wait()
	return nil
}

// evictionWorker handles eviction requests serially
func (m *IndexManager) evictionWorker() {
	defer m.wg.Done()
	for req := range m.evictCh {
		req.result <- m.doEvict(req.key)
	}
}

// doEvict performs the actual eviction
func (m *IndexManager) doEvict(key RepositoryKey) error {
	repo, exists := m.repositories.Load(key)
	if !exists {
		return nil
	}
	// Acquire write lock to ensure no readers are active
	repo.readersMu.Lock()
	defer repo.readersMu.Unlock()
	// Remove tracking metadata and underlying storage
	m.repositories.Delete(key)
	return util.RemoveAll(m.fs, repo.path)
}

// GetRepository returns a repository, fetching it if necessary
func (m *IndexManager) GetRepository(ctx context.Context, key RepositoryKey) (*RepositoryHandle, error) {
	cached := &cachedRepository{
		key:  key,
		path: m.getRepositoryPath(key),
	}
	// Try to store, or load existing
	actual, _ := m.repositories.LoadOrStore(key, cached)
	// Initialize the repository (guaranteed to be run exactly once)
	if err := actual.initialize(ctx, m); err != nil {
		m.repositories.Delete(key)
		return nil, err
	}
	// Check if it needs updating (only for current repositories)
	if key.Type == CurrentIndex && time.Since(time.UnixMilli(actual.lastUpdate.Load())) > m.currentUpdateInterval {
		if err := m.updateRepository(ctx, actual); err != nil {
			return nil, err
		}
	}
	return m.openRepository(actual)
}

// updateRepository updates an existing repository
func (m *IndexManager) updateRepository(ctx context.Context, repo *cachedRepository) error {
	// NOTE: If readers are active, this attempt to write lock will block
	repo.readersMu.Lock()
	defer repo.readersMu.Unlock()
	// Update using fetcher
	repoFs, err := m.fs.Chroot(repo.path)
	if err != nil {
		return err
	}
	defer repo.lastUpdate.Store(time.Now().UnixMilli())
	return repo.fetcher.Update(ctx, repoFs)
}

// openRepository opens a repository for reading
func (m *IndexManager) openRepository(cached *cachedRepository) (*RepositoryHandle, error) {
	// Take a read lock for the duration of the function to prevent eviction
	cached.readersMu.RLock()
	defer cached.readersMu.RUnlock()
	// Update access metadata
	cached.lastAccess.Store(time.Now().UnixMilli())
	// Open repository from filesystem
	repoFs, err := m.fs.Chroot(cached.path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to chroot to repo")
	}
	storer := filesystem.NewStorage(repoFs, cache.NewObjectLRUDefault())
	repo, err := git.Open(storer, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open repository")
	}
	// NOTE: Take additional read lock that will remain until cleanup is called.
	cached.readersMu.RLock()
	return &RepositoryHandle{
		Repository: repo,
		cleanup: func() {
			cached.readersMu.RUnlock()
		},
	}, nil
}

// RepositoryHandle wraps a *git.Repository with a cleanup function
type RepositoryHandle struct {
	*git.Repository
	cleanup     func()
	cleanupOnce sync.Once
}

func (r *RepositoryHandle) Close() error {
	r.cleanupOnce.Do(r.cleanup)
	return nil
}

// maybeEvictSnapshot evicts the least recently used snapshot if at capacity
func (m *IndexManager) maybeEvictSnapshot(ctx context.Context) error {
	// Count current snapshots
	snapshotCount := 0
	var lruSnapshot *cachedRepository
	var lruKey RepositoryKey
	m.repositories.Range(func(key RepositoryKey, repo *cachedRepository) bool {
		if key.Type == SnapshotIndex {
			snapshotCount++
			if lruSnapshot == nil || repo.lastAccess.Load() < lruSnapshot.lastAccess.Load() {
				lruSnapshot = repo
				lruKey = key
			}
		}
		return true
	})
	// NOTE: During initialization, the new repo will already be added so we expect `snapshotCount == existing + 1`
	if snapshotCount <= m.maxSnapshots {
		return nil
	}
	// Send eviction request
	result := make(chan error, 1)
	select {
	case m.evictCh <- evictRequest{key: lruKey, result: result}:
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getRepositoryPath returns the filesystem path for a repository
func (m *IndexManager) getRepositoryPath(key RepositoryKey) string {
	switch key.Type {
	case CurrentIndex:
		return "current"
	case SnapshotIndex:
		return "snapshots/" + key.Name
	default:
		panic(errors.Errorf("unknown repository type: %v", key.Type).Error())
	}
}
