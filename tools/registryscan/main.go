// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
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
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	reg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pkg/errors"
)

type Package struct {
	Name    string
	Version string
	Path    string
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("Usage:")
		fmt.Println("  go run main.go <path_to_lock_file> <path_to_git_repo>")
		fmt.Println("  go run main.go <package@version> <path_to_git_repo>")
		fmt.Println("Examples:")
		fmt.Println("  go run main.go ./Cargo.lock /path/to/crates-index")
		fmt.Println("  go run main.go serde@1.0.2 /path/to/crates-index")
		os.Exit(1)
	}

	firstArg := os.Args[1]
	repoPath := os.Args[2]

	ctx := context.Background()
	var lockfile string
	var err error
	if packageAtVersionRegex.MatchString(firstArg) {
		lockfile, err = downloadCargoLock(ctx, firstArg)
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
	}
	packages, err := parseCargoLockContent(lockfile)
	if err != nil {
		fmt.Printf("Error parsing packages: %v\n", err)
		os.Exit(1)
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		fmt.Printf("Error opening git repository: %v\n", err)
		os.Exit(1)
	}

	// XXX: Needs to be manually adjusted to account for published date.
	published := time.Now()
	commit, err := findCommitWithVersions(repo, packages, published)
	if err != nil {
		fmt.Printf("Error finding commit: %v\n", err)
		os.Exit(1)
	}

	if commit != nil {
		fmt.Printf("Found commit: %s\n", commit.Hash)
	} else {
		fmt.Println("No commit found containing all package versions")
	}
}

var packageAtVersionRegex = regexp.MustCompile(`^([a-zA-Z0-9_-]+)@([0-9]+\.[0-9]+\.[0-9]+.*)$`)

func downloadCargoLock(ctx context.Context, packageSpec string) (string, error) {
	matches := packageAtVersionRegex.FindStringSubmatch(packageSpec)
	if len(matches) != 3 {
		return "", fmt.Errorf("invalid package specification: %s (expected format: package@version)", packageSpec)
	}
	name := matches[1]
	version := matches[2]
	fmt.Printf("Downloading %s@%s...\n", name, version)
	registry := &reg.HTTPRegistry{Client: http.DefaultClient}
	reader, err := registry.Artifact(ctx, name, version)
	if err != nil {
		return "", fmt.Errorf("failed to download crate: %v", err)
	}
	defer reader.Close()
	cargoLockContent, err := extractCargoLockFromTarGz(reader)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract Cargo.lock")
	}
	if cargoLockContent == "" {
		return "", errors.Errorf("crate %s@%s does not contain a Cargo.lock file", name, version)
	}
	fmt.Printf("Successfully extracted Cargo.lock\n")
	return cargoLockContent, nil
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
	maxFound := matchesAt(int(published.Unix()))
	// Linear scan backwards in days to find the one containing the target registry state.
	day := 24 * time.Hour
	dayBound := day
	for ; ; dayBound += day {
		if matchesAt(int(published.Add(-dayBound).Unix())) < maxFound {
			break
		}
	}
	lowerBound := int(published.Add(-dayBound).Unix())
	// Binary search through the day's commits to find the earliest target registry state.
	ts, found := sort.Find(int(day.Seconds()), func(ts int) int {
		f := matchesAt(lowerBound + ts)
		return -cmp.Compare(f, maxFound)
	})
	if !found {
		panic("not found")
	}
	// Repeat the commit query to find the same commit found above.
	t := time.Unix(int64(lowerBound+ts), 0)
	commitIter, err := repo.Log(&git.LogOptions{Until: &t})
	if err != nil {
		panic(err)
	}
	return commitIter.Next()
}
