// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/internal/safememfs"
)

func TestCancelledAcquisitionReleasesRepositoryHandle(t *testing.T) {
	upstreamDir := t.TempDir()
	must(gitxtest.CreateRepoFromYAML(`
commits:
  - id: initial
    branch: snapshot-2024-01-01
    files:
      config.json: '{}'
`, &gitxtest.RepositoryOptions{
		Storer: filesystem.NewStorage(osfs.New(upstreamDir), cache.NewObjectLRUDefault()),
	}))
	oldArchiveURL := archiveIndexURL
	archiveIndexURL = "file://" + upstreamDir
	t.Cleanup(func() { archiveIndexURL = oldArchiveURL })

	started := make(chan struct{})
	release := make(chan struct{})
	clone := func(_ context.Context, s storage.Storer, fs billy.Filesystem, opts *git.CloneOptions) (*git.Repository, error) {
		close(started)
		<-release
		return git.CloneContext(context.Background(), s, fs, opts)
	}
	mgr := NewIndexManager(IndexManagerConfig{
		Filesystem:            safememfs.New(),
		MaxSnapshots:          1,
		CurrentUpdateInterval: time.Hour,
		SnapshotCloneFunc:     clone,
	})
	t.Cleanup(func() { _ = mgr.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	key := RepositoryKey{Type: SnapshotIndex, Name: "2024-01-01"}
	result := make(chan error, 1)
	go func() {
		_, err := mgr.GetRepository(ctx, key)
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; err == nil {
		t.Fatal("GetRepository error = nil after cancellation")
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if repo, ok := mgr.repositories.Load(key); ok && repo.rwMutex.TryLock() {
			repo.rwMutex.Unlock()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("repository read lock remained held after the caller canceled")
}
