// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/api"
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
	publishedTime, err := time.Parse(time.RFC3339, req.PublishedTime)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to parse published_time"))
	}
	lockfileData, err := base64.StdEncoding.DecodeString(req.LockfileBase64)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to decode lockfile"))
	}
	packages, err := cargolock.Parse(string(lockfileData))
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "failed to parse Cargo.lock"))
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
		needsCurrent = req.PublishedTime > mostRecentSnapshotDate
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
	resolution, err := index.FindRegistryResolutionMultiRepo(repos, packages, publishedTime)
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

// getRelevantSnapshots determines which snapshots are relevant based on publish date
// Snapshots are referred to by their end date so we include:
// 1. [If present] The snapshot where [publish-buffer, publish] contains the end date
// 2. [If present] The snapshot where [startDate, endDate] "contains" the publish date (where startDate is the previous snapshot)
func getRelevantSnapshots(snapshots []string, publishDate time.Time) []string {
	var relevant []string
	buffer := 14 * 24 * time.Hour // 14 day buffer
	// Find the first snapshot before and the first after publishDate.
	// Since snapshots are sorted chronologically, we can process them in order.
	var beforeSnapshot, afterSnapshot string
	published := publishDate.Format("2006-01-02")
	for _, snapshotEnd := range snapshots {
		if snapshotEnd < published {
			beforeSnapshot = snapshotEnd
		} else {
			afterSnapshot = snapshotEnd
			break
		}
	}
	// If present, add the first snapshot after publish date
	if afterSnapshot != "" {
		relevant = append(relevant, afterSnapshot)
	}
	// If present, add the before snapshot provided it's within the buffer
	if beforeSnapshot != "" && beforeSnapshot > publishDate.Add(-buffer).Format("2006-01-02") {
		relevant = append(relevant, beforeSnapshot)
	}
	return relevant
}
