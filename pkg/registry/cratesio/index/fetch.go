// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"regexp"
	"sort"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/pkg/errors"
)

var (
	currentIndexURL = "https://github.com/rust-lang/crates.io-index.git"
	archiveIndexURL = "https://github.com/rust-lang/crates.io-index-archive.git"
)

var snapshotBranchRegex = regexp.MustCompile(`^refs/heads/snapshot-(\d{4}-\d{2}-\d{2})$`)

// ListAvailableSnapshots queries the archive repository for available snapshots
// Snapshots are returned as their associated RFC3339 date e.g. 2025-06-14.
func ListAvailableSnapshots(ctx context.Context) ([]string, error) {
	// Create a remote reference to list branches
	rem := git.NewRemote(nil, &config.RemoteConfig{URLs: []string{archiveIndexURL}})
	// List the references
	refs, err := rem.ListContext(ctx, &git.ListOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list remote refs")
	}
	var snapshots []string
	for _, ref := range refs {
		if matches := snapshotBranchRegex.FindStringSubmatch(ref.Name().String()); matches != nil {
			snapshots = append(snapshots, matches[1])
		}
	}
	sort.Strings(snapshots)
	return snapshots, nil
}

// Fetcher defines how to fetch and update a repository index
type Fetcher interface {
	// Fetch clones the repository into the given filesystem
	Fetch(ctx context.Context, fs billy.Filesystem) error
	// Update updates an existing repository in the filesystem
	Update(ctx context.Context, fs billy.Filesystem) error
}

// CurrentIndexFetcher fetches the current crates.io index
type CurrentIndexFetcher struct{}

func (f *CurrentIndexFetcher) Fetch(ctx context.Context, fs billy.Filesystem) error {
	storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	_, err := git.CloneContext(ctx, storer, nil, &git.CloneOptions{
		URL:           currentIndexURL,
		ReferenceName: plumbing.Master,
		SingleBranch:  true,
		NoCheckout:    true,
	})
	if err != nil {
		return errors.Wrap(err, "failed to clone current index")
	}
	// Nice-to-have: Set HEAD to track the remote since it will remain up-to-date on Update.
	remoteMain := plumbing.NewRemoteReferenceName(git.DefaultRemoteName, "master")
	err = storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, remoteMain))
	if err != nil {
		return errors.Wrap(err, "failed to configure HEAD")
	}
	return nil
}

func (f *CurrentIndexFetcher) Update(ctx context.Context, fs billy.Filesystem) error {
	storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	repo, err := git.Open(storer, nil)
	if err != nil {
		return errors.Wrap(err, "failed to open repository")
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{Force: true})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	} else if err != nil {
		return errors.Wrap(err, "failed to fetch updates")
	}
	return nil
}

// SnapshotIndexFetcher fetches a specific snapshot branch
type SnapshotIndexFetcher struct {
	Date string
}

func (f *SnapshotIndexFetcher) Fetch(ctx context.Context, fs billy.Filesystem) error {
	storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	_, err := git.CloneContext(ctx, storer, nil, &git.CloneOptions{
		URL:           archiveIndexURL,
		ReferenceName: plumbing.NewBranchReferenceName("snapshot-" + f.Date),
		SingleBranch:  true,
		NoCheckout:    true,
	})
	if err != nil {
		return errors.Wrapf(err, "failed to clone snapshot %s", f.Date)
	}
	return nil
}

func (f *SnapshotIndexFetcher) Update(ctx context.Context, fs billy.Filesystem) error {
	// Snapshots are immutable
	return nil
}
