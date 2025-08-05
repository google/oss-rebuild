// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/syncx"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/pkg/errors"
)

// IndexType represents the type of repository
type IndexType int

const (
	CurrentIndex IndexType = iota
	SnapshotIndex
)

func (t IndexType) String() string {
	switch t {
	case CurrentIndex:
		return "current"
	case SnapshotIndex:
		return "snapshot"
	default:
		return "unknown"
	}
}

// RepositoryKey uniquely identifies a repository
type RepositoryKey struct {
	Type IndexType
	Name string // date string for snapshots, empty for current
}

// managedRepository represents a repository with centralized state management
type managedRepository struct {
	key        RepositoryKey
	path       string
	lastAccess atomic.Int64 // UnixMilli timestamp
	lastUpdate atomic.Int64 // UnixMilli timestamp
	fetcher    Fetcher
	// rwMutex protects repository from concurrent eviction
	rwMutex sync.RWMutex
}

// acquisitionRequest represents a request to acquire one or more repositories
type acquisitionRequest struct {
	keys     []RepositoryKey
	response chan acquisitionResponse
	ctx      context.Context
}

// acquisitionResponse contains the result of an acquisition request
type acquisitionResponse struct {
	handles []*RepositoryHandle
	err     error
}

// IndexManager manages multiple repository versions with centralized coordination
type IndexManager struct {
	fs                    billy.Filesystem
	maxSnapshots          int
	currentUpdateInterval time.Duration
	repositories          syncx.Map[RepositoryKey, *managedRepository]
	acquisitionCh         chan acquisitionRequest
	shutdownCh            chan struct{}
	acquireWg             sync.WaitGroup
}

// IndexManagerConfig configures the IndexManager
type IndexManagerConfig struct {
	Filesystem            billy.Filesystem
	MaxSnapshots          int
	CurrentUpdateInterval time.Duration
}

// NewIndexManager creates a new index manager with coordinator model
func NewIndexManager(cfg IndexManagerConfig) *IndexManager {
	m := &IndexManager{
		fs:                    cfg.Filesystem,
		maxSnapshots:          cfg.MaxSnapshots,
		currentUpdateInterval: cfg.CurrentUpdateInterval,
		repositories:          syncx.Map[RepositoryKey, *managedRepository]{},
		acquisitionCh:         make(chan acquisitionRequest),
		shutdownCh:            make(chan struct{}),
	}
	m.acquireWg.Add(1)
	go m.acquireLoop()
	return m
}

// NewIndexManagerFromFS creates a new index manager and loads existing repositories
func NewIndexManagerFromFS(cfg IndexManagerConfig) (*IndexManager, error) {
	m := NewIndexManager(cfg)
	// Load existing repositories synchronously during startup
	if err := m.loadExistingRepositories(); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// loadExistingRepositories loads pre-existing repositories from filesystem
func (m *IndexManager) loadExistingRepositories() error {
	// Load current index if it exists
	if err := m.loadExistingRepo(RepositoryKey{Type: CurrentIndex}); err != nil {
		return err
	}
	// Load existing snapshots
	snapshotBase := "snapshots"
	entries, err := m.fs.ReadDir(snapshotBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No snapshots directory, that's fine
		}
		return errors.Wrapf(err, "failed to read snapshots directory at %s", snapshotBase)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			key := RepositoryKey{Type: SnapshotIndex, Name: entry.Name()}
			if err := m.loadExistingRepo(key); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadExistingRepo checks a path for a valid git repository and adds it
func (m *IndexManager) loadExistingRepo(key RepositoryKey) error {
	path := m.getRepositoryPath(key)
	repoFs, err := m.fs.Chroot(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil // Path doesn't exist, skip
	}
	storer := filesystem.NewStorage(repoFs, cache.NewObjectLRUDefault())
	if _, err := git.Open(storer, nil); err != nil {
		return nil // Not a valid repo, skip
	}
	repo := &managedRepository{
		key:  key,
		path: path,
	}
	switch key.Type {
	case CurrentIndex:
		repo.fetcher = &CurrentIndexFetcher{}
	case SnapshotIndex:
		repo.fetcher = &SnapshotIndexFetcher{Date: key.Name}
	}
	f, err := m.fs.Stat(path)
	if err != nil {
		return nil // Unable to stat, skip
	}
	repo.lastUpdate.Store(f.ModTime().UnixMilli())
	m.repositories.Store(key, repo)
	return nil
}

// acquireLoop is the main event loop that handles all state changes
func (m *IndexManager) acquireLoop() {
	defer m.acquireWg.Done()
	for {
		select {
		case req := <-m.acquisitionCh:
			m.handleAcquisitionRequest(req)
		case <-m.shutdownCh:
			return
		}
	}
}

// handleAcquisitionRequest processes a request to acquire repositories
func (m *IndexManager) handleAcquisitionRequest(req acquisitionRequest) {
	ctx, cancel := context.WithCancel(req.ctx)
	defer cancel() // ensure the context is cleaned up
	// Construct the sets of existing and needed repositories
	var existing, missing []*managedRepository
	seen := make(map[RepositoryKey]bool, len(req.keys))
	for _, key := range req.keys {
		if _, exists := seen[key]; exists {
			req.response <- acquisitionResponse{err: errors.Errorf("duplicate key requested: %v", key)}
			return
		}
		if repo, exists := m.repositories.Load(key); exists {
			existing = append(existing, repo)
			repo.lastAccess.Store(time.Now().UnixMilli())
		} else {
			repo = &managedRepository{
				key:  key,
				path: m.getRepositoryPath(key),
			}
			switch key.Type {
			case CurrentIndex:
				repo.fetcher = &CurrentIndexFetcher{}
			case SnapshotIndex:
				repo.fetcher = &SnapshotIndexFetcher{Date: key.Name}
			default:
				req.response <- acquisitionResponse{err: errors.Errorf("unknown repository type: %v", key.Type)}
				return
			}
			repo.lastAccess.Store(time.Now().UnixMilli())
			missing = append(missing, repo)
		}
	}
	// Ensure we have enough space to fetch the new repositories
	if err := m.evictSnapshotsIfNeeded(ctx, missing, existing); err != nil {
		req.response <- acquisitionResponse{err: errors.Wrap(err, "evicting snapshots")}
		return
	}
	// Register repository readers
	var locked syncx.Map[RepositoryKey, *managedRepository]
	unlock := func() {
		for _, repo := range locked.Iter() {
			repo.rwMutex.RUnlock()
		}
	}
	// Get read locks for existing
	for _, repo := range existing {
		repo.rwMutex.RLock()
		locked.Store(repo.key, repo)
		// TODO: This should be executed in parallel with the fetches below
		if repo.key.Type == CurrentIndex && time.UnixMilli(repo.lastUpdate.Load()).Before(time.Now().Add(-m.currentUpdateInterval)) {
			if err := m.doUpdate(ctx, repo); err != nil {
				unlock()
				req.response <- acquisitionResponse{err: errors.Wrap(err, "updating current index")}
				return
			}
		}
	}
	// Fetch the new repositories and get read locks
	var causeErr error
	for err := range pipe.ParInto(len(missing), pipe.FromSlice(missing), func(in *managedRepository, out chan<- error) {
		in.rwMutex.Lock()
		var err error
		if foo, exists := m.repositories.LoadOrStore(in.key, in); exists {
			in = foo
		} else {
			err = m.doInitialFetch(ctx, in)
			if err != nil {
				m.repositories.Delete(in.key)
			}
			in.rwMutex.Unlock()
		}
		in.rwMutex.RLock()
		locked.Store(in.key, in)
		out <- errors.Wrapf(err, "fetching %v", in.key)
	}).Out() {
		if causeErr == nil && err != nil {
			cancel()
			causeErr = err
		}
	}
	if causeErr != nil {
		unlock()
		req.response <- acquisitionResponse{err: causeErr}
		return
	}
	// Get handles
	var handles []*RepositoryHandle
	for _, repo := range locked.Iter() {
		handle, err := m.createRepositoryHandle(repo)
		if err != nil {
			unlock()
			req.response <- acquisitionResponse{err: causeErr}
			return
		}
		handles = append(handles, handle)
	}
	req.response <- acquisitionResponse{handles: handles}
}

// createRepositoryHandle creates a handle for an active repository
func (m *IndexManager) createRepositoryHandle(repo *managedRepository) (*RepositoryHandle, error) {
	repoFs, err := m.fs.Chroot(repo.path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to chroot to repo")
	}
	// NOTE: Create a separate object cache for each handle since it is not threadsafe
	storer := filesystem.NewStorage(repoFs, cache.NewObjectLRUDefault())
	gitRepo, err := git.Open(storer, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open repository")
	}
	return &RepositoryHandle{
		Repository: gitRepo,
		key:        repo.key,
		release: func() {
			if repo, exists := m.repositories.Load(repo.key); exists {
				repo.rwMutex.RUnlock()
			}
		},
	}, nil
}

// evictSnapshotsIfNeeded evicts snapshots with LRU preference
func (m *IndexManager) evictSnapshotsIfNeeded(ctx context.Context, toAllocate, toKeep []*managedRepository) error {
	var all, candidates []*managedRepository
	m.repositories.Range(func(key RepositoryKey, repo *managedRepository) bool {
		if repo.key.Type == SnapshotIndex {
			if !slices.Contains(toAllocate, repo) && !slices.Contains(toKeep, repo) {
				candidates = append(candidates, repo)
			}
			all = append(all, repo)
		}
		return true
	})
	// Calculate how many to evict
	toEvict := len(all) + len(toAllocate) - m.maxSnapshots
	if toEvict <= 0 {
		return nil
	}
	if len(candidates) < len(toAllocate) {
		return errors.Errorf("insufficient snapshots available to evict: [need=%d,available=%d]", len(toAllocate), len(candidates))
	}
	// Sort by last access time (LRU first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastAccess.Load() < candidates[j].lastAccess.Load()
	})
	// Try in a loop until reached number (avoid busy waiting with backoff)
	for evicted := 0; evicted < toEvict; {
		didEvict := false
		// Prefer LRU but allow others if LRU reads are active
		for _, snapshot := range candidates {
			if _, exists := m.repositories.Load(snapshot.key); !exists {
				continue // Already evicted
			}
			if snapshot.rwMutex.TryLock() {
				// Successfully acquired write lock - evict this snapshot
				m.repositories.Delete(snapshot.key)
				util.RemoveAll(m.fs, snapshot.path)
				snapshot.rwMutex.Unlock()
				didEvict = true
				break
			}
		}
		if didEvict {
			evicted++
		} else {
			// Back off a few ms when we fail to make progress
			select {
			case <-time.After(10 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// doInitialFetch performs the initial repository fetch
func (m *IndexManager) doInitialFetch(ctx context.Context, repo *managedRepository) error {
	repoFs, err := m.fs.Chroot(repo.path)
	if err != nil {
		return errors.Wrap(err, "failed to create repo directory")
	}
	if err := repo.fetcher.Fetch(ctx, repoFs); err != nil {
		util.RemoveAll(m.fs, repo.path)
		return err
	}
	repo.lastUpdate.Store(time.Now().UnixMilli())
	return nil
}

// doUpdate performs a repository update (for already-fetched repositories)
func (m *IndexManager) doUpdate(ctx context.Context, repo *managedRepository) error {
	repoFs, err := m.fs.Chroot(repo.path)
	if err != nil {
		return errors.Wrap(err, "failed to access repo directory")
	}
	if err := repo.fetcher.Update(ctx, repoFs); err != nil {
		return err
	}
	repo.lastUpdate.Store(time.Now().UnixMilli())
	return nil
}

// GetRepository requests a single repository (convenience method)
func (m *IndexManager) GetRepository(ctx context.Context, key RepositoryKey) (*RepositoryHandle, error) {
	handles, err := m.GetRepositories(ctx, []RepositoryKey{key})
	if err != nil {
		return nil, err
	}
	return handles[0], nil
}

// GetRepositories atomically acquires multiple repositories
func (m *IndexManager) GetRepositories(ctx context.Context, keys []RepositoryKey) ([]*RepositoryHandle, error) {
	response := make(chan acquisitionResponse, 1)

	select {
	case m.acquisitionCh <- acquisitionRequest{keys: keys, response: response, ctx: ctx}:
		select {
		case resp := <-response:
			return resp.handles, resp.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down index manager to new acquisitions
func (m *IndexManager) Close() error {
	// Signal shutdown
	close(m.shutdownCh)
	// Wait for last acquisition to finish
	m.acquireWg.Wait()
	return nil
}

// getRepositoryPath returns the filesystem path for a repository
func (m *IndexManager) getRepositoryPath(key RepositoryKey) string {
	switch key.Type {
	case CurrentIndex:
		return "current"
	case SnapshotIndex:
		return path.Join("snapshots", key.Name)
	default:
		panic(errors.Errorf("unknown repository type: %v", key.Type).Error())
	}
}

// RepositoryHandle wraps a *git.Repository with centralized cleanup
type RepositoryHandle struct {
	*git.Repository
	key         RepositoryKey
	release     func()
	releaseOnce sync.Once
}

func (r *RepositoryHandle) Close() error {
	r.releaseOnce.Do(r.release)
	return nil
}
