// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/cargolock"
	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
	"github.com/pkg/errors"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Println("Usage:")
		fmt.Println("  go run main.go <path_to_lock_file> <cache_dir>")
		fmt.Println("  go run main.go <package@version> <cache_dir>")
		fmt.Println("Examples:")
		fmt.Println("  go run main.go ./Cargo.lock /path/to/cache")
		fmt.Println("  go run main.go serde@1.0.2 /path/to/cache")
		os.Exit(1)
	}
	firstArg := os.Args[1]
	cacheDir := os.Args[2]

	ctx := context.Background()
	var lockfile string
	var published time.Time
	var err error
	if packageAtVersionRegex.MatchString(firstArg) {
		lockfile, published, err = downloadCargoLockWithDate(ctx, firstArg)
		if err != nil {
			fmt.Printf("Error downloading package: %v\n", err)
			os.Exit(1)
		}
	} else {
		file, err := os.Open(firstArg)
		if err != nil {
			fmt.Printf("Error opening Cargo.lock file: %v\n", err)
			os.Exit(1)
		}
		defer file.Close()
		contentBytes, err := io.ReadAll(file)
		if err != nil {
			fmt.Printf("Error reading Cargo.lock file: %v\n", err)
			os.Exit(1)
		}
		lockfile = string(contentBytes)
		published = time.Now() // Default to current time for file-based input
	}
	packages, err := cargolock.Parse(lockfile)
	if err != nil {
		fmt.Printf("Error parsing packages: %v\n", err)
		os.Exit(1)
	}
	manager, err := setupIndexManager(cacheDir)
	if err != nil {
		fmt.Printf("Error setting up index manager: %v\n", err)
		os.Exit(1)
	}
	defer manager.Close()
	resolution, err := findRegistryResolution(ctx, manager, packages, published)
	if err != nil {
		fmt.Printf("Error finding resolution: %v\n", err)
		os.Exit(1)
	}
	if resolution != nil {
		fmt.Printf("Found commit: %s at time: %s\n", resolution.CommitHash, resolution.CommitTime.Format(time.RFC3339))
	} else {
		fmt.Println("No commit found containing all package versions")
	}
}

var packageAtVersionRegex = regexp.MustCompile(`^([a-zA-Z0-9_-]+)@([0-9]+\.[0-9]+\.[0-9]+.*)$`)

func setupIndexManager(cacheDir string) (*index.IndexManager, error) {
	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, errors.Wrap(err, "failed to create cache directory")
	}
	// Create filesystem for cache directory
	fs := osfs.New(cacheDir)
	// Setup index manager
	cfg := index.IndexManagerConfig{
		Filesystem:            fs,
		MaxSnapshots:          10,               // Allow up to 10 snapshots to be cached
		CurrentUpdateInterval: 30 * time.Minute, // Update current index every 30 minutes
	}
	return index.NewIndexManagerFromFS(cfg)
}

func getRelevantSnapshots(snapshots []string, publishDate time.Time) []string {
	var relevant []string
	buffer := 14 * 24 * time.Hour // 14 day buffer
	var beforeSnapshot string
	var afterSnapshot string
	// Since snapshots are sorted chronologically, we can process them in order
	for _, snapshotDate := range snapshots {
		// Parse the date string (format: YYYY-MM-DD)
		date, err := time.Parse("2006-01-02", snapshotDate)
		if err != nil {
			fmt.Printf("Warning: invalid date format in snapshot %s: %v\n", snapshotDate, err)
			continue
		}
		if date.Before(publishDate) {
			// Keep track of the most recent snapshot before publish date
			beforeSnapshot = snapshotDate
		} else {
			// This is the first snapshot on or after publish date
			afterSnapshot = snapshotDate
			break
		}
	}
	// Add the before snapshot if it's within the 14-day buffer
	if beforeSnapshot != "" {
		beforeDate, _ := time.Parse("2006-01-02", beforeSnapshot)
		if beforeDate.After(publishDate.Add(-buffer)) {
			relevant = append(relevant, beforeSnapshot)
		}
	}
	// Add the first snapshot after publish date
	if afterSnapshot != "" {
		relevant = append(relevant, afterSnapshot)
	}
	return relevant
}

func findRegistryResolution(ctx context.Context, manager *index.IndexManager, packages []cargolock.Package, published time.Time) (*index.RegistryResolution, error) {
	// Get available snapshots to determine which repositories to use
	snapshots, err := index.ListAvailableSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get available snapshots: %v", err)
	}
	// Determine which snapshots are relevant based on publish date
	relevantSnapshots := getRelevantSnapshots(snapshots, published)
	// Determine if we need the current repository
	var needsCurrent bool
	if len(snapshots) == 0 {
		needsCurrent = true
	} else {
		// Parse the most recent snapshot date (snapshots are sorted chronologically)
		mostRecentSnapshotDate := snapshots[len(snapshots)-1]
		mostRecentDate, err := time.Parse("2006-01-02", mostRecentSnapshotDate)
		if err != nil {
			fmt.Printf("Warning: invalid date format in most recent snapshot %s: %v\n", mostRecentSnapshotDate, err)
			needsCurrent = true // Default to including current if we can't parse
		} else {
			needsCurrent = published.After(mostRecentDate)
		}
	}
	// Build list of repositories in order (current first, then snapshots)
	var keys []index.RepositoryKey
	var repos []*git.Repository
	if needsCurrent {
		fmt.Printf("Including current repository in search...\n")
		keys = append(keys, index.RepositoryKey{Type: index.CurrentIndex})
	}
	// Add relevant snapshots in reverse chronological order (newest first)
	for i := len(relevantSnapshots) - 1; i >= 0; i-- {
		snapshotDate := relevantSnapshots[i]
		fmt.Printf("Including snapshot repository %s in search...\n", snapshotDate)
		keys = append(keys, index.RepositoryKey{Type: index.SnapshotIndex, Name: snapshotDate})
	}
	handles, err := manager.GetRepositories(ctx, keys)
	if err != nil {
		return nil, errors.Wrap(err, "fetching repositories")
	}
	for _, handle := range handles {
		defer handle.Close()
		repos = append(repos, handle.Repository)
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repositories available for search")
	}
	return index.FindRegistryResolutionMultiRepo(repos, packages, published)
}

func downloadCargoLockWithDate(ctx context.Context, packageSpec string) (string, time.Time, error) {
	matches := packageAtVersionRegex.FindStringSubmatch(packageSpec)
	if len(matches) != 3 {
		return "", time.Time{}, fmt.Errorf("invalid package specification: %s (expected format: package@version)", packageSpec)
	}
	name := matches[1]
	version := matches[2]
	registry := &reg.HTTPRegistry{Client: http.DefaultClient}
	fmt.Printf("Fetching metadata for %s@%s...\n", name, version)
	crate, err := registry.Crate(ctx, name)
	if err != nil {
		return "", time.Time{}, errors.Wrap(err, "failed to fetch crate metadata")
	}
	var publishDate time.Time
	found := false
	for _, v := range crate.Versions {
		if v.Version == version {
			publishDate = v.Created
			found = true
			break
		}
	}
	if !found {
		return "", time.Time{}, errors.Errorf("version %s not found for crate %s", version, name)
	}
	fmt.Printf("Found publish date: %s\n", publishDate.Format(time.RFC3339))
	fmt.Printf("Downloading %s@%s...\n", name, version)
	reader, err := registry.Artifact(ctx, name, version)
	if err != nil {
		return "", time.Time{}, errors.Wrap(err, "failed to download crate")
	}
	defer reader.Close()
	cargoLockContent, err := extractCargoLockFromTarGz(reader)
	if err != nil {
		return "", time.Time{}, errors.Wrap(err, "failed to extract Cargo.lock")
	}
	if cargoLockContent == "" {
		return "", time.Time{}, errors.Errorf("crate %s@%s does not contain a Cargo.lock file", name, version)
	}
	fmt.Printf("Successfully extracted Cargo.lock\n")
	return cargoLockContent, publishDate, nil
}

func extractCargoLockFromTarGz(reader io.Reader) (string, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return "", err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(header.Name, "/Cargo.lock") || header.Name == "Cargo.lock" {
			content, err := io.ReadAll(tarReader)
			if err != nil {
				return "", err
			}
			return string(content), nil
		}
	}
	return "", nil // No Cargo.lock found
}
