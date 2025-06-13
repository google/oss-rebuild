// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package main implements a CLI tool to combine multiple benchmark files.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys) // Sort versions
	return keys
}

func combineBenchmarks(in []io.ReadCloser) (*benchmark.PackageSet, error) {
	latestUpdated := time.Time{}
	// mergedPackagData[Ecosystem][Package][Version][Artifact] == true
	// When artiact is unspecified, the map will be empty:
	// mergedPackagData[Ecosystem][Package][Version] == map[string]bool{}
	mergedPackageData := make(map[string]map[string]map[string]map[string]bool)
	for _, inReader := range in {
		var ps benchmark.PackageSet
		err := json.NewDecoder(inReader).Decode(&ps)
		if err != nil {
			return nil, errors.Wrap(err, "decoding input file")
		}
		if err := inReader.Close(); err != nil {
			return nil, errors.Wrap(err, "closing input file")
		}
		if ps.Metadata.Updated.After(latestUpdated) {
			latestUpdated = ps.Metadata.Updated
		}
		for _, inputPkg := range ps.Packages {
			if _, ok := mergedPackageData[inputPkg.Ecosystem]; !ok {
				mergedPackageData[inputPkg.Ecosystem] = make(map[string]map[string]map[string]bool)
			}
			eco := mergedPackageData[inputPkg.Ecosystem]
			if _, ok := eco[inputPkg.Name]; !ok {
				eco[inputPkg.Name] = make(map[string]map[string]bool)
			}
			pkg := eco[inputPkg.Name]
			if len(inputPkg.Artifacts) != 0 && len(inputPkg.Versions) != len(inputPkg.Artifacts) {
				return nil, errors.Errorf("Data integrity error in for package %s: Artifacts list must be empty or match versions count.", inputPkg.Name)
			}
			for i, v := range inputPkg.Versions {
				if _, ok := pkg[v]; !ok {
					pkg[v] = make(map[string]bool)
				}
				vers := pkg[v]
				if len(inputPkg.Artifacts) > 0 {
					vers[inputPkg.Artifacts[i]] = true
				}
			}
		}
	}

	final := benchmark.PackageSet{
		Metadata: benchmark.Metadata{
			Count:   0,
			Updated: latestUpdated,
		},
		Packages: []benchmark.Package{},
	}
	for _, e := range sortedKeys(mergedPackageData) {
		for _, p := range sortedKeys(mergedPackageData[e]) {
			pkg := benchmark.Package{
				Ecosystem: e,
				Name:      p,
				Versions:  []string{},
				Artifacts: []string{},
			}
			var versionsWithArtifact int
			for v := range mergedPackageData[e][p] {
				if len(mergedPackageData[e][p][v]) > 0 {
					versionsWithArtifact++
				}
			}
			if versionsWithArtifact > 0 && versionsWithArtifact < len(mergedPackageData[e][p]) {
				log.Fatalf("Data error, package %s contains some version with artifacts and some without.", p)
			}
			for _, v := range sortedKeys(mergedPackageData[e][p]) {
				if len(mergedPackageData[e][p][v]) == 0 {
					pkg.Versions = append(pkg.Versions, v)
					final.Metadata.Count++
				} else {
					for _, a := range sortedKeys(mergedPackageData[e][p][v]) {
						pkg.Versions = append(pkg.Versions, v)
						pkg.Artifacts = append(pkg.Artifacts, a)
						final.Metadata.Count++
					}
				}
			}
			final.Packages = append(final.Packages, pkg)
		}
	}
	return &final, nil
}

func main() {
	flag.Parse()

	inFiles := flag.Args()
	if len(inFiles) == 0 {
		log.Fatal("Error: At least one input benchmark file path must be provided as an argument.")
	}
	var inReaders []io.ReadCloser
	for _, in := range inFiles {
		in := strings.TrimSpace(in)
		if in == "" {
			continue
		}
		f, err := os.Open(in)
		if err != nil {
			log.Fatalf("Failed to read %s", in)
		}
		inReaders = append(inReaders, f)
	}
	combined, err := combineBenchmarks(inReaders)
	if err != nil {
		log.Fatalf("Failed to combine benchmarks: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(combined); err != nil {
		log.Fatalf("Failed to encode combined package set: %v", err)
	}
}
