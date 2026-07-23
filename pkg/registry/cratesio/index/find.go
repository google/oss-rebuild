// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
	"github.com/pkg/errors"
)

// RegistryResolution represents the point in registry history where a Cargo.lock can be best reconstructed.
type RegistryResolution struct {
	// CommitHash is the git commit hash of the optimal registry state
	CommitHash plumbing.Hash
	// CommitTime is when this registry state was committed
	CommitTime time.Time
}

// FindConfig configures the behavior of FindRegistryResolution
type FindConfig struct {
	// VerboseLogging enables detailed logging of commit analysis during search
	VerboseLogging bool
}

// FindRegistryResolution searches across multiple sequential registry indices for the earliest possible state a registry resolution could have taken place.
// Indices should be ordered from newest to oldest (e.g., current index first, then previous snapshot(s)).
// An optional config parameter can be provided to control logging and other behaviors.
func FindRegistryResolution(indices []*git.Repository, lockfileCrates []cargolock.Package, cratePublished time.Time, cfg *FindConfig) (*RegistryResolution, error) {
	return findRegistryResolution(indices, lockfileCrates, nil, &cratePublished, cfg)
}

// FindRegistryResolutionAtPackage anchors the lock search at the target
// package's first index-visible commit in the newest supplied index.
func FindRegistryResolutionAtPackage(indices []*git.Repository, lockfileCrates []cargolock.Package, target cargolock.Package, cfg *FindConfig) (*RegistryResolution, error) {
	if len(indices) == 0 {
		return nil, errors.New("no registry indices to search")
	}
	head, err := indices[0].Head()
	if err != nil {
		return nil, errors.Wrap(err, "reading registry HEAD")
	}
	headHash := head.Hash()
	targetResolution, err := findRegistryResolution(indices[:1], []cargolock.Package{target}, &headHash, nil, cfg)
	if err != nil {
		if errors.Is(err, errNoMatches) {
			return nil, ErrTargetPackageNotFound
		}
		return nil, errors.Wrap(err, "finding target in registry")
	}
	return findRegistryResolution(indices, lockfileCrates, &targetResolution.CommitHash, nil, cfg)
}

func findRegistryResolution(indices []*git.Repository, lockfileCrates []cargolock.Package, upperCommit *plumbing.Hash, upperTime *time.Time, cfg *FindConfig) (*RegistryResolution, error) {
	if len(lockfileCrates) == 0 {
		return nil, errors.New("no crates to resolve")
	}
	// Convert to internal format
	internalPackages := make([]internalPackage, len(lockfileCrates))
	for i, pkg := range lockfileCrates {
		internalPackages[i] = internalPackage{
			Package: pkg,
			Path:    EntryPath(pkg.Name),
		}
	}
	var lastResult, bestResult *searchResult
	// Search each index in order until found
	for i, index := range indices {
		var from *plumbing.Hash
		if i == 0 {
			from = upperCommit
		}
		result, err := findCommitWithVersions(index, internalPackages, from, upperTime, cfg)
		if err != nil {
			if i > 0 && err == errNoMatches {
				// Edge case: For multi-index searches, subsequent indices may lack matches:
				// Repo 0 @ last commit = 1, Repo 1 @ first commit = 0
				continue
			}
			return nil, errors.Wrap(err, "searching index")
		}
		if i == 0 && result.ResolvableCrates != len(internalPackages) {
			return nil, errors.Errorf("registry search upper bound contains %d of %d packages", result.ResolvableCrates, len(internalPackages))
		}
		if lastResult != nil {
			// Edge case: If the previous repo didn't find a boundary and this one
			// has fewer crates, we've found our transition point:
			// Repo 0 @ last commit = 5, Repo 1 @ first commit = 4
			if lastResult.PriorCommit == nil && result.ResolvableCrates < lastResult.ResolvableCrates {
				break
			}
		}
		bestResult = result
		// If we found a boundary within this index, we're done
		if result.PriorCommit != nil {
			break
		}
		lastResult = result
	}
	// Convert to public API format
	return &RegistryResolution{
		CommitHash: bestResult.ResolutionCommit.Hash,
		CommitTime: bestResult.ResolutionCommit.Committer.When,
	}, nil
}

// --- Internal Implementation ---

type internalPackage struct {
	cargolock.Package
	Path string
}

type searchResult struct {
	ResolutionCommit *object.Commit
	ResolvableCrates int
	PriorCommit      *object.Commit
}

// EntryPath computes the crates registry path for a crate
func EntryPath(name string) string {
	name = strings.ToLower(name)
	switch len(name) {
	case 1:
		return filepath.Join("1", name)
	case 2:
		return filepath.Join("2", name)
	case 3:
		return filepath.Join("3", string(name[0]), name)
	default:
		return filepath.Join(name[:2], name[2:4], name)
	}
}

var errNoMatches = errors.New("no packages found at search upper bound")

// ErrTargetPackageNotFound indicates that the target package version is not
// present in the newest supplied registry segment.
var ErrTargetPackageNotFound = errors.New("target package version not found")

func findCommitWithVersions(repo *git.Repository, packages []internalPackage, upperCommit *plumbing.Hash, upperTime *time.Time, cfg *FindConfig) (*searchResult, error) {
	blobHashes := make(map[string]plumbing.Hash)
	present := make(map[string]map[string]bool)
	packagesByPath := make(map[string][]internalPackage)
	for _, pkg := range packages {
		packagesByPath[pkg.Path] = append(packagesByPath[pkg.Path], pkg)
	}
	matchesFor := func(commit *object.Commit) int {
		tree, err := commit.Tree()
		if err != nil {
			return 0
		}
		var found int
		for path, pathPackages := range packagesByPath {
			entry, err := tree.FindEntry(path)
			if err != nil {
				continue
			}
			if entry.Hash != blobHashes[path] {
				blob, err := repo.BlobObject(entry.Hash)
				if err != nil {
					continue
				}
				reader, err := blob.Reader()
				if err != nil {
					continue
				}
				content, err := io.ReadAll(reader)
				reader.Close()
				if err != nil {
					continue
				}
				blobHashes[path] = entry.Hash
				present[path] = make(map[string]bool, len(pathPackages))
				for _, pkg := range pathPackages {
					present[path][pkg.Version] = bytes.Contains(content, []byte(`"vers":"`+pkg.Version+`"`))
				}
			}
			for _, pkg := range pathPackages {
				if present[path][pkg.Version] {
					found++
				}
			}
		}
		if cfg != nil && cfg.VerboseLogging {
			log.Printf("Analyzed %s [%s]: Found %d matches", commit.Hash.String(), commit.Committer.When.UTC().Format(time.RFC3339), found)
		}
		return found
	}
	// Get a single iterator for the available history up to the requested bound.
	// The default order is reverse chronological, which is what we want.
	logOpts := &git.LogOptions{}
	if upperCommit != nil {
		logOpts.From = *upperCommit
	} else if upperTime != nil {
		logOpts.Until = upperTime
	}
	commitIter, err := repo.Log(logOpts)
	if err != nil {
		return nil, err
	}
	defer commitIter.Close()
	// Analyze the very first commit to establish the baseline number of matches.
	firstCommit, err := commitIter.Next()
	if err == io.EOF {
		return nil, errors.New("no commits found before search upper bound")
	} else if err != nil {
		return nil, err
	}
	maxFound := matchesFor(firstCommit)
	if maxFound == 0 {
		return nil, errNoMatches
	}
	// These variables will define the time window for the fine-grained search.
	upperBoundCommit := firstCommit
	// Scan backwards through commits, analyzing one commit per ~24-hour window
	// until we find a drop in the number of matches.
	day := 24 * time.Hour
	nextCheckTime := firstCommit.Committer.When.Add(-day)
	for c, err := range iterx.ToSeq2(commitIter, io.EOF) {
		if err != nil {
			return nil, errors.Wrap(err, "iterating over daily commits")
		}
		if c.Committer.When.Before(nextCheckTime) {
			if matchesFor(c) < maxFound {
				break
			}
			upperBoundCommit = c
			nextCheckTime = c.Committer.When.Add(-day)
		}
	}
	// Scan backwards through the remaining commits to find the exact drop.
	// This is also required when the coarse scan reaches the repository root:
	// the unexamined tail may be shorter than one day.
	commitIter, err = repo.Log(&git.LogOptions{From: upperBoundCommit.Hash})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}
	defer commitIter.Close()
	var lastCommit *object.Commit
	for commit, err := range iterx.ToSeq2(commitIter, io.EOF) {
		if err != nil {
			return nil, errors.Wrap(err, "iterating over commits")
		}
		if matchesFor(commit) < maxFound {
			return &searchResult{
				ResolutionCommit: lastCommit,
				ResolvableCrates: maxFound,
				PriorCommit:      commit,
			}, nil
		}
		lastCommit = commit
	}
	return &searchResult{
		ResolutionCommit: lastCommit,
		ResolvableCrates: maxFound,
		PriorCommit:      nil,
	}, nil
}
