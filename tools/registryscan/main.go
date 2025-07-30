// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pkg/errors"
)

type Package struct {
	Name    string
	Version string
	Path    string
}

type RepoCache struct {
	CacheDir  string
	Current   *git.Repository
	Snapshots map[string]*git.Repository // date -> repo
}

type SnapshotInfo struct {
	Date time.Time
	Name string
}

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
	packages, err := parseCargoLockContent(lockfile)
	if err != nil {
		fmt.Printf("Error parsing packages: %v\n", err)
		os.Exit(1)
	}
	cache, err := setupRepoCache(cacheDir, published)
	if err != nil {
		fmt.Printf("Error setting up repo cache: %v\n", err)
		os.Exit(1)
	}
	commit, repo, err := findCommitWithVersionsInCache(cache, packages, published)
	if err != nil {
		fmt.Printf("Error finding commit: %v\n", err)
		os.Exit(1)
	}
	if commit != nil {
		fmt.Printf("Found commit: %s in repo: %s\n", commit.Hash, getRepoName(cache, repo))
	} else {
		fmt.Println("No commit found containing all package versions")
	}
}

var packageAtVersionRegex = regexp.MustCompile(`^([a-zA-Z0-9_-]+)@([0-9]+\.[0-9]+\.[0-9]+.*)$`)

const (
	CURRENT_INDEX_URL = "https://github.com/rust-lang/crates.io-index.git"
	ARCHIVE_INDEX_URL = "https://github.com/rust-lang/crates.io-index-archive.git"
)

func setupRepoCache(cacheDir string, publishDate time.Time) (*RepoCache, error) {
	cache := &RepoCache{
		CacheDir:  cacheDir,
		Snapshots: make(map[string]*git.Repository),
	}
	// Ensure cache directory exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, errors.Wrap(err, "failed to create cache directory")
	}
	// Get available snapshots and setup relevant ones
	snapshots, err := getAvailableSnapshots()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get available snapshots")
	}
	// Determine which snapshots we need based on publish date
	relevantSnapshots := getRelevantSnapshots(snapshots, publishDate)
	// Only setup current repo if publish date is after the most recent snapshot
	// (current contains all history from most recent snapshot until now)
	var needsCurrent bool
	if len(snapshots) == 0 {
		// No snapshots available, we need current
		// NOTE: Might want to make this case an error.
		needsCurrent = true
	} else {
		// Get the most recent snapshot date
		mostRecentSnapshot := snapshots[len(snapshots)-1]
		needsCurrent = publishDate.After(mostRecentSnapshot.Date)
	}
	if needsCurrent {
		currentPath := filepath.Join(cacheDir, "current")
		currentRepo, err := setupCurrentRepo(currentPath)
		if err != nil {
			return nil, errors.Wrap(err, "failed to setup current repo")
		}
		cache.Current = currentRepo
	}
	for _, snapshot := range relevantSnapshots {
		snapshotPath := filepath.Join(cacheDir, snapshot.Name)
		repo, err := setupSnapshotRepo(snapshotPath, snapshot.Name)
		if err != nil {
			fmt.Printf("Warning: failed to setup snapshot %s: %v\n", snapshot.Name, err)
			continue
		}
		cache.Snapshots[snapshot.Name] = repo
	}
	return cache, nil
}

func getAvailableSnapshots() ([]SnapshotInfo, error) {
	// Create a remote reference to list branches
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{ARCHIVE_INDEX_URL},
	})
	// List the references
	refs, err := rem.List(&git.ListOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list remote refs")
	}
	var snapshots []SnapshotInfo
	snapshotRegex := regexp.MustCompile(`^refs/heads/snapshot-(\d{4}-\d{2}-\d{2})$`)
	for _, ref := range refs {
		if matches := snapshotRegex.FindStringSubmatch(ref.Name().String()); matches != nil {
			dateStr := matches[1]
			date, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				fmt.Printf("Warning: invalid date format in snapshot %s: %v\n", ref.Name(), err)
				continue
			}
			snapshots = append(snapshots, SnapshotInfo{
				Date: date,
				Name: "snapshot-" + dateStr,
			})
		}
	}
	// Sort snapshots by date
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Date.Before(snapshots[j].Date)
	})
	return snapshots, nil
}

func getRelevantSnapshots(snapshots []SnapshotInfo, publishDate time.Time) []SnapshotInfo {
	var relevant []SnapshotInfo
	buffer := 14 * 24 * time.Hour // 14 day buffer
	var beforeSnapshot *SnapshotInfo
	var afterSnapshot *SnapshotInfo
	// Since snapshots are sorted, we can process them in order
	for i, snapshot := range snapshots {
		if snapshot.Date.Before(publishDate) {
			// Keep track of the most recent snapshot before publish date
			beforeSnapshot = &snapshots[i]
		} else {
			// This is the first snapshot on or after publish date
			afterSnapshot = &snapshots[i]
			break
		}
	}
	// Add the before snapshot if it's within the 14-day buffer
	if beforeSnapshot != nil && beforeSnapshot.Date.After(publishDate.Add(-buffer)) {
		relevant = append(relevant, *beforeSnapshot)
	}
	// Add the first snapshot after publish date
	if afterSnapshot != nil {
		relevant = append(relevant, *afterSnapshot)
	}
	return relevant
}

func setupCurrentRepo(repoPath string) (*git.Repository, error) {
	// Check if repo already exists
	if repo, err := git.PlainOpen(repoPath); err == nil {
		// Repo exists, try to fetch updates (for bare repos, use remote fetch)
		fmt.Printf("Updating current index repository...\n")
		remote, err := repo.Remote("origin")
		if err == nil {
			// Force reset to handle the case where a snapshot has been taken
			err = remote.Fetch(&git.FetchOptions{Force: true})
			if err != nil && err != git.NoErrAlreadyUpToDate {
				fmt.Printf("Warning: failed to fetch updates: %v\n", err)
			}
		}
		return repo, nil
	}
	// Clone the current index repo
	fmt.Printf("Cloning current index repository...\n")
	repo, err := git.PlainClone(repoPath, true, &git.CloneOptions{
		URL:           CURRENT_INDEX_URL,
		ReferenceName: plumbing.Master,
		SingleBranch:  true,
		NoCheckout:    true,
		Progress:      os.Stdout,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to clone current repo")
	}
	return repo, nil
}

func setupSnapshotRepo(repoPath, branchName string) (*git.Repository, error) {
	// Check if repo already exists
	if repo, err := git.PlainOpen(repoPath); err == nil {
		return repo, nil
	}
	// Clone the snapshot repo with single branch
	fmt.Printf("Cloning snapshot repository: %s...\n", branchName)
	repo, err := git.PlainClone(repoPath, true, &git.CloneOptions{
		URL:           ARCHIVE_INDEX_URL,
		ReferenceName: plumbing.NewBranchReferenceName(branchName),
		SingleBranch:  true,
		NoCheckout:    true,
		Progress:      os.Stdout,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to clone snapshot repo %s", branchName)
	}
	return repo, nil
}

func findCommitWithVersionsInCache(cache *RepoCache, packages []Package, published time.Time) (*object.Commit, *git.Repository, error) {
	// First try the current repo if it exists (most likely to have recent packages)
	if cache.Current != nil {
		fmt.Printf("Searching in current repository...\n")
		if commit, err := findCommitWithVersions(cache.Current, packages, published); err == nil && commit != nil {
			return commit, cache.Current, nil
		} else if err != nil {
			fmt.Printf("Error searching current repo: %v\n", err)
		}
	}
	// Then try snapshot repositories
	for snapshotName, repo := range cache.Snapshots {
		fmt.Printf("Searching in snapshot repository: %s...\n", snapshotName)
		if commit, err := findCommitWithVersions(repo, packages, published); err == nil && commit != nil {
			return commit, repo, nil
		} else if err != nil {
			fmt.Printf("Error searching snapshot %s: %v\n", snapshotName, err)
		}
	}
	return nil, nil, nil
}

func getRepoName(cache *RepoCache, repo *git.Repository) string {
	if repo == cache.Current {
		return "current"
	}
	for snapshotName, snapshotRepo := range cache.Snapshots {
		if repo == snapshotRepo {
			return snapshotName
		}
	}
	return "unknown"
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

func parseCargoLockContent(content string) ([]Package, error) {
	var packages []Package
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "name = ") {
			name := strings.Trim(strings.TrimPrefix(line, "name = "), "\"")
			if scanner.Scan() {
				versionLine := scanner.Text()
				if strings.HasPrefix(versionLine, "version = ") {
					version := strings.Trim(strings.TrimPrefix(versionLine, "version = "), "\"")
					packages = append(packages, Package{Name: name, Version: version, Path: getPackageFilePath(name)})
				}
			}
		}
	}
	fmt.Println(packages)

	return packages, scanner.Err()
}

func getPackageFilePath(packageName string) string {
	packageName = strings.ToLower(packageName)
	switch len(packageName) {
	case 1:
		return filepath.Join("1", packageName)
	case 2:
		return filepath.Join("2", packageName)
	case 3:
		return filepath.Join("3", string(packageName[0]), packageName)
	default:
		return filepath.Join(packageName[:2], packageName[2:4], packageName)
	}
}

func findCommitWithVersions(repo *git.Repository, packages []Package, published time.Time) (*object.Commit, error) {
	fmt.Println("Calculating commits...")
	blobHashes := make(map[string]plumbing.Hash)
	present := make(map[string]bool)
	// TODO: detect yanking
	matchesFor := func(commit *object.Commit) int {
		fmt.Printf("Analyzing %s [time: %s]... ", commit.Hash.String(), commit.Committer.When)
		tree, err := commit.Tree()
		if err != nil {
			panic(err)
		}
		var found int
		for _, pkg := range packages {
			entry, err := tree.FindEntry(pkg.Path)
			if err != nil {
				continue
			}
			if entry.Hash != blobHashes[pkg.Path] {
				blob, err := repo.BlobObject(entry.Hash)
				if err != nil {
					panic(err)
				}
				reader, err := blob.Reader()
				if err != nil {
					panic(err)
				}
				defer reader.Close()
				content, err := io.ReadAll(reader)
				if err != nil {
					panic(err)
				}
				blobHashes[pkg.Path] = entry.Hash
				present[pkg.Path] = bytes.Contains(content, []byte(`"vers":"`+pkg.Version+`"`))
			}

			if present[pkg.Path] {
				found++
			}
		}
		fmt.Println("Found", found)
		return found
	}
	matchesAt := func(ts int) int {
		t := time.Unix(int64(ts), 0)
		// NOTE: Log ordering shouldn't be an issue with the index's linear history.
		commitIter, err := repo.Log(&git.LogOptions{Until: &t})
		if err != nil {
			panic(err)
		}
		commit, err := commitIter.Next()
		if err == io.EOF {
			return 0
		} else if err != nil {
			panic(err)
		}
		return matchesFor(commit)
	}
	maxFound := matchesAt(int(published.Unix()))
	// Linear scan backwards in days to find the one containing the target registry state.
	day := 24 * time.Hour
	dayBound := day
	for ; ; dayBound += day {
		if matchesAt(int(published.Add(-dayBound).Unix())) < maxFound {
			break
		}
	}
	upperBoundT := published.Add(-dayBound + day)
	lowerBoundT := published.Add(-dayBound)
	commitIter, err := repo.Log(&git.LogOptions{Since: &lowerBoundT, Until: &upperBoundT})
	if err != nil {
		panic(err)
	}
	var lastCommit *object.Commit
	for {
		commit, err := commitIter.Next()
		if err != nil {
			return nil, err
		}
		if matchesFor(commit) < maxFound {
			break
		}
		lastCommit = commit
	}
	return lastCommit, nil
}
