// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Backfill artifacts for benchmark data files that lack Artifacts entries.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"slices"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// BenchmarkPackage represents a package entry in the benchmark data file.
type BenchmarkPackage struct {
	Ecosystem string   `json:"Ecosystem"`
	Name      string   `json:"Name"`
	Versions  []string `json:"Versions"`
	Artifacts []string `json:"Artifacts,omitempty"`
}

// BenchmarkData represents the structure of a benchmark data file.
type BenchmarkData struct {
	Count    int                 `json:"Count"`
	Updated  string              `json:"Updated"`
	Packages []*BenchmarkPackage `json:"Packages"`
}

// failureRecord tracks a failed artifact guess.
type failureRecord struct {
	pkg     *BenchmarkPackage
	version string
	index   int
	err     error
}

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatalf("Usage: %s <path-to-benchmark-data-file>", os.Args[0])
	}
	dataPath := flag.Arg(0)
	if err := backfillArtifacts(dataPath); err != nil {
		log.Fatalf("Failed to backfill artifacts: %v", err)
	}
}

func backfillArtifacts(dataPath string) error {
	ctx := context.Background()
	// Load the benchmark data file
	data, err := loadBenchmarkData(dataPath)
	if err != nil {
		return fmt.Errorf("loading benchmark data: %w", err)
	}
	// Set up registry mux with cached HTTP client
	mux := meta.NewRegistryMux(httpx.NewCachedClient(http.DefaultClient, &cache.CoalescingMemoryCache{}))
	// Create rate limiters per ecosystem (matching benchmark defaults)
	limiters := map[rebuild.Ecosystem]*ratex.BackoffLimiter{
		rebuild.Debian:   ratex.NewBackoffLimiter(1000 * time.Millisecond),
		rebuild.PyPI:     ratex.NewBackoffLimiter(200 * time.Millisecond),
		rebuild.NPM:      ratex.NewBackoffLimiter(200 * time.Millisecond),
		rebuild.Maven:    ratex.NewBackoffLimiter(700 * time.Millisecond),
		rebuild.CratesIO: ratex.NewBackoffLimiter(1500 * time.Millisecond),
	}
	// Count total work needed
	totalWork := 0
	packagesNeedingArtifacts := []*BenchmarkPackage{}
	for _, pkg := range data.Packages {
		if len(pkg.Artifacts) == 0 && len(pkg.Versions) > 0 {
			totalWork += len(pkg.Versions)
			packagesNeedingArtifacts = append(packagesNeedingArtifacts, pkg)
		}
	}
	if totalWork == 0 {
		fmt.Println("No packages need artifact backfill.")
		return nil
	}
	fmt.Printf("Backfilling artifacts for %d packages (%d total versions)...\n", len(packagesNeedingArtifacts), totalWork)
	// Create progress bar
	bar := pb.New(totalWork)
	bar.Output = os.Stderr
	bar.ShowTimeLeft = true
	bar.Start()
	// Track failures
	var failures []failureRecord
	// Backfill artifacts for each package
	for _, pkg := range packagesNeedingArtifacts {
		ecosystem := rebuild.Ecosystem(pkg.Ecosystem)
		limiter := limiters[ecosystem]
		// Initialize artifacts slice
		pkg.Artifacts = make([]string, len(pkg.Versions))
		// Query GuessArtifact for each version
		for i, version := range pkg.Versions {
			// Rate limit
			if err := limiter.Wait(ctx); err != nil {
				return fmt.Errorf("rate limiter wait: %w", err)
			}
			target := rebuild.Target{
				Ecosystem: ecosystem,
				Package:   pkg.Name,
				Version:   version,
			}
			artifact, err := meta.GuessArtifact(ctx, target, mux)
			if err != nil {
				// Log the failure and track it
				log.Printf("FAILED: %s@%s: %v", pkg.Name, version, err)
				failures = append(failures, failureRecord{
					pkg:     pkg,
					version: version,
					index:   i,
					err:     err,
				})
				limiter.Backoff() // Back off on failure
			} else {
				pkg.Artifacts[i] = artifact
				limiter.Success() // Adaptive rate limiting
			}
			bar.Increment()
		}
	}
	bar.Finish()
	// Handle failures if any
	if len(failures) > 0 {
		fmt.Printf("\n%d failures occurred during backfill:\n", len(failures))
		for _, f := range failures {
			fmt.Printf("  - %s@%s: %v\n", f.pkg.Name, f.version, f.err)
		}
		// Prompt user to confirm removal
		fmt.Printf("\nRemove %d failed entries from the output? (y/n): ", len(failures))
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading user input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response == "y" || response == "yes" {
			if err := removeFailedEntries(data, failures); err != nil {
				return fmt.Errorf("removing failed entries: %w", err)
			}
			fmt.Printf("Removed %d failed entries.\n", len(failures))
		} else {
			fmt.Println("Keeping failed entries (they will have empty artifact strings).")
		}
	}
	// Update timestamp
	data.Updated = time.Now().UTC().Format(time.RFC3339)
	// Save the updated data back to the file
	if err := saveBenchmarkData(dataPath, data); err != nil {
		return fmt.Errorf("saving benchmark data: %w", err)
	}
	fmt.Printf("Successfully backfilled artifacts in %s\n", dataPath)
	return nil
}

func removeFailedEntries(data *BenchmarkData, failures []failureRecord) error {
	// Build a map of packages to indices to remove
	pkgToIndices := make(map[*BenchmarkPackage][]int)
	for _, f := range failures {
		pkgToIndices[f.pkg] = append(pkgToIndices[f.pkg], f.index)
	}
	// Process each affected package
	packagesToRemove := make(map[*BenchmarkPackage]bool)
	for pkg, indices := range pkgToIndices {
		// Sort indices in descending order to remove from end to start
		// This prevents index shifting issues
		for i := range indices {
			for j := i + 1; j < len(indices); j++ {
				if indices[i] < indices[j] {
					indices[i], indices[j] = indices[j], indices[i]
				}
			}
		}
		// Remove versions and artifacts at the failed indices
		for _, idx := range indices {
			pkg.Versions = slices.Delete(pkg.Versions, idx, idx+1)
			pkg.Artifacts = slices.Delete(pkg.Artifacts, idx, idx+1)
		}
		// Mark package for removal if it has no versions left
		if len(pkg.Versions) == 0 {
			packagesToRemove[pkg] = true
		}
	}
	// Remove empty packages from the data
	if len(packagesToRemove) > 0 {
		newPackages := make([]*BenchmarkPackage, 0, len(data.Packages))
		for _, pkg := range data.Packages {
			if !packagesToRemove[pkg] {
				newPackages = append(newPackages, pkg)
			}
		}
		data.Packages = newPackages
	}
	// Recalculate Count
	totalVersions := 0
	for _, pkg := range data.Packages {
		totalVersions += len(pkg.Versions)
	}
	data.Count = totalVersions
	return nil
}

func loadBenchmarkData(path string) (*BenchmarkData, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()
	var data BenchmarkData
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}
	return &data, nil
}

func saveBenchmarkData(path string, data *BenchmarkData) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}
