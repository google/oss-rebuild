// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"encoding/base64"
	"slices"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// TODO: Request and Response types should be defined elsewhere to separate Stub users from the implementaiton.
// This is currently not possible without introducing a cyclic dependency since
// they are intended to be used from pkg/rebuild/cratesio which is itself a
// dependency of pkg/rebuild/ Breaking that dependency requires
// separating StrategyOneOf and the dependent types into their own package.

// FindRegistryCommitRequest represents a request to find a registry commit
type FindRegistryCommitRequest struct {
	LockfileBase64 string `form:"lockfile_base64"`
	PublishedTime  string `form:"published_time"`
}

func (r FindRegistryCommitRequest) Validate() error {
	if r.LockfileBase64 == "" {
		return errors.New("lockfile_base64 is required")
	}
	if r.PublishedTime == "" {
		return errors.New("published_time is required")
	}
	if _, err := time.Parse(time.RFC3339, r.PublishedTime); err != nil {
		return errors.Wrap(err, "invalid published_time format, must be RFC3339")
	}
	if _, err := base64.StdEncoding.DecodeString(r.LockfileBase64); err != nil {
		return errors.Wrap(err, "invalid lockfile_base64 encoding")
	}
	return nil
}

// FindRegistryCommitResponse represents the response from registry commit resolution
type FindRegistryCommitResponse struct {
	CommitHash string `json:"commit_hash"`
}

// FindRegistryCommitDeps holds dependencies for the registry commit resolution service
type FindRegistryCommitDeps struct {
	IndexManager *index.IndexManager
}

// FindRegistryCommit finds a registry commit hash based on a Cargo.lock file and publish time
func FindRegistryCommit(ctx context.Context, req FindRegistryCommitRequest, deps *FindRegistryCommitDeps) (*FindRegistryCommitResponse, error) {
	// Validate request
	lockfileData, err := base64.StdEncoding.DecodeString(req.LockfileBase64)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to decode lockfile"))
	}
	publishedTime, err := time.Parse(time.RFC3339, req.PublishedTime)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to parse published_time"))
	}
	// Parse Cargo.lock content
	packages, err := cargolock.Parse(string(lockfileData))
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to parse Cargo.lock"))
	}
	packages = cargolock.CratesIOPackages(packages)
	if len(packages) == 0 {
		return &FindRegistryCommitResponse{}, nil
	}
	// Determine which snapshots should be searched based on publish date
	snapshots, err := index.ListAvailableSnapshots(ctx)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "failed to get available snapshots"))
	}
	relevantSnapshots := getRelevantSnapshots(snapshots, publishedTime)
	// Determine if we need the current repository
	var needsCurrent bool
	if len(relevantSnapshots) == 0 {
		needsCurrent = true
	} else {
		mostRecentSnapshotDate := relevantSnapshots[0]
		needsCurrent = publishedTime.Format("2006-01-02") >= mostRecentSnapshotDate
	}
	// Build list of repositories in newest-to-oldest order
	var keys []index.RepositoryKey
	if needsCurrent {
		keys = append(keys, index.RepositoryKey{Type: index.CurrentIndex})
	}
	for _, snapshotDate := range relevantSnapshots {
		keys = append(keys, index.RepositoryKey{Type: index.SnapshotIndex, Name: snapshotDate})
	}
	if len(keys) == 0 {
		return &FindRegistryCommitResponse{}, nil
	}
	// Fetch index repositories, ensuring publishedTime is contained in the current registry
	opts := &index.RepoOpt{Contains: &publishedTime}
	handles, err := deps.IndexManager.GetRepositories(ctx, keys, opts)
	if err != nil {
		if outOfDateErr, ok := err.(*index.RegistryOutOfDateError); ok {
			// Calculate retry-after based on repository update timing
			retryAfter := time.Until(outOfDateErr.NextUpdateTime)
			if retryAfter <= 0 {
				// If next update is overdue, use an additional update interval as fallback
				retryAfter = outOfDateErr.UpdateInterval
			}
			// Cap the retry after to a reasonable maximum (1 hour)
			maxRetry := time.Hour
			if retryAfter > maxRetry {
				retryAfter = maxRetry
			}
			return nil, api.AsStatus(codes.Unavailable, errors.New("current index registry requires update"), api.RetryAfter(retryAfter))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "failed to get repositories"))
	}
	defer func() {
		for _, handle := range handles {
			handle.Close()
		}
	}()
	var repos []*git.Repository
	for _, handle := range handles {
		repos = append(repos, handle.Repository)
	}
	// Find the registry resolution
	resolution, err := index.FindRegistryResolution(repos, packages, publishedTime, nil)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "failed to find registry resolution"))
	}
	if resolution == nil {
		return &FindRegistryCommitResponse{}, nil
	}
	return &FindRegistryCommitResponse{
		CommitHash: resolution.CommitHash.String(),
	}, nil
}

const (
	// Maximum number of preceding snapshots to include before publish date.
	// A request searches at most maxPrecedingSnapshots+1 snapshot indices (up to
	// maxPrecedingSnapshots before publishDate plus the next snapshot after),
	// which must fit within the index manager's MaxSnapshots limit so a single
	// lookup never evicts its own inputs.
	maxPrecedingSnapshots = 3
)

// getRelevantSnapshots determines which snapshots are relevant based on publish date.
// Snapshots are referred to by their end date so we include:
// 1. Up to maxPrecedingSnapshots snapshots ending on or before publish date
// 2. [If present] The next snapshot that ends strictly after publish date
func getRelevantSnapshots(snapshots []string, publishDate time.Time) []string {
	searchEnd := publishDate.Format("2006-01-02")
	var before []string
	var after string
	for _, snapshotEnd := range snapshots {
		if snapshotEnd > searchEnd {
			after = snapshotEnd
			break
		}
		before = append(before, snapshotEnd)
	}
	if len(before) > maxPrecedingSnapshots {
		before = before[len(before)-maxPrecedingSnapshots:]
	}
	relevant := slices.Clone(before)
	if after != "" {
		relevant = append(relevant, after)
	}
	// Reverse to get newest-to-oldest order
	slices.Reverse(relevant)
	return relevant
}
