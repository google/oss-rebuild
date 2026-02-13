// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/google/oss-rebuild/pkg/registry/debian"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
)

// --- Structs for the Snapshot API Response ---

type SnapshotResult struct {
	Version string `json:"version"`
}

type SnapshotResponse struct {
	Result []SnapshotResult `json:"result"`
}

// --- Configuration ---

const (
	APIUrlTemplate = "https://snapshot.debian.org/mr/package/%s/"
	MaxConcurrency = 5 // Limit concurrent requests to be polite to the API
)

// IndexedPackage allows us to preserve the original order of packages after parallel processing.
type IndexedPackage struct {
	Index int
	Pkg   benchmark.Package
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <benchmark_file.json>")
		os.Exit(1)
	}
	inputFileName := os.Args[1]

	// 1. Read the input file using the benchmark library
	fmt.Printf("Reading %s...\n", inputFileName)
	ps, err := benchmark.ReadBenchmark(inputFileName)
	if err != nil {
		panic(fmt.Sprintf("Error reading benchmark file: %v", err))
	}

	// 2. Prepare indexed packages for the pipeline
	var indexedPackages []IndexedPackage
	for i, p := range ps.Packages {
		indexedPackages = append(indexedPackages, IndexedPackage{Index: i, Pkg: p})
	}

	client := &http.Client{Timeout: 30 * time.Second}

	fmt.Printf("Processing %d packages with concurrency limit %d...\n", len(ps.Packages), MaxConcurrency)

	// 3. Initialize progress bar
	bar := pb.New(len(ps.Packages))
	bar.ShowTimeLeft = true
	bar.Start()

	// 4. Create and run the pipeline
	// We use pipe.FromSlice to start the pipe, and ParDo for parallel processing.
	p := pipe.FromSlice(indexedPackages)

	processedPipe := p.ParDo(MaxConcurrency, func(ip IndexedPackage, out chan<- IndexedPackage) {
		// Always update progress bar when one item is done processing (sent to out)
		defer bar.Increment()

		// Only process debian ecosystem
		if ip.Pkg.Ecosystem != "debian" {
			out <- ip
			return
		}

		pkgName := ip.Pkg.Name
		if parts := strings.Split(ip.Pkg.Name, "/"); len(parts) > 1 {
			pkgName = parts[1]
		}

		// Fetch snapshot data
		versionsMap, err := fetchSnapshotVersions(client, pkgName)
		if err != nil {
			// In case of error, we just keep the original package but maybe log it?
			// Since we want to display a progress bar, we avoid printing to stdout directly.
			// Ideally we could collect errors. For now, we proceed.
			log.Println(err)
			out <- ip
			return
		}

		// Update versions
		for vIdx, localVer := range ip.Pkg.Versions {
			bareLocal := localVer
			if _, after, found := strings.Cut(localVer, ":"); found {
				bareLocal = after
			}
			if fullVerWithEpoch, exists := versionsMap[bareLocal]; exists {
				if localVer != fullVerWithEpoch {
					ip.Pkg.Versions[vIdx] = fullVerWithEpoch
				}
			}
		}
		out <- ip
	})

	// 5. Collect results
	var results []IndexedPackage
	for res := range processedPipe.Out() {
		results = append(results, res)
	}
	bar.Finish()

	// 6. Sort results to restore original order
	sort.Slice(results, func(i, j int) bool {
		return results[i].Index < results[j].Index
	})

	// 7. Reconstruct the PackageSet
	ps.Packages = make([]benchmark.Package, len(results))
	for i, res := range results {
		ps.Packages[i] = res.Pkg
	}

	// 8. Write the file back out
	fmt.Println("Writing updated data back to file...")
	outputBytes, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("Error marshalling output: %v", err))
	}

	if err := os.WriteFile(inputFileName, outputBytes, 0644); err != nil {
		panic(fmt.Sprintf("Error writing file: %v", err))
	}

	fmt.Println("Done.")
}

// fetchSnapshotVersions returns a map where:
// Key = Version WITHOUT epoch (e.g., "3.1.9")
// Value = Version WITH epoch (e.g., "1:3.1.9")
// If the API result has no epoch, the zero epoch is added to Value.
func fetchSnapshotVersions(client *http.Client, pkgName string) (map[string]string, error) {
	url := fmt.Sprintf(APIUrlTemplate, pkgName)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Go-Script-Updater/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status code %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var snapshotResp SnapshotResponse
	if err := json.Unmarshal(body, &snapshotResp); err != nil {
		return nil, err
	}
	versionMap := make(map[string]string)
	for _, res := range snapshotResp.Result {
		fullVersion := res.Version
		if v, err := debian.ParseVersion(fullVersion); err == nil {
			// Fill in the implicit zero epochs
			if v.Epoch == "" {
				v.Epoch = "0"
			}
			versionMap[v.Epochless()] = v.String()
		}
	}
	return versionMap, nil
}
