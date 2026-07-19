// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cargolock

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

const cratesIOIndexSource = "registry+https://github.com/rust-lang/crates.io-index"

// Package represents a package entry from a Cargo.lock file.
type Package struct {
	Name    string
	Version string
	Source  string
}

// Lockfile represents a parsed Cargo.lock file.
type Lockfile struct {
	// FormatVersion is the top-level `version` field of the lock file.
	// 0 means the version header was absent (implying format v1).
	FormatVersion int
	Packages      []Package
}

// CratesIOPackages returns packages resolved from the crates.io registry.
func (lf Lockfile) CratesIOPackages() []Package {
	var packages []Package
	for _, pkg := range lf.Packages {
		if pkg.Source == cratesIOIndexSource {
			packages = append(packages, pkg)
		}
	}
	return packages
}

// ParseLockfile parses a Cargo.lock file, returning the format version and packages.
func ParseLockfile(content string) (Lockfile, error) {
	var lf Lockfile
	var current *Package
	flushPackage := func() {
		if current != nil && current.Name != "" && current.Version != "" {
			lf.Packages = append(lf.Packages, *current)
		}
		current = nil
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" || line == "[root]" {
			flushPackage()
			current = &Package{}
			continue
		}
		if strings.HasPrefix(line, "[") {
			flushPackage()
			continue
		}
		// Top-level format version: `version = N` (unquoted integer, unlike package versions).
		if current == nil && strings.HasPrefix(line, "version = ") && !strings.Contains(line, "\"") {
			fmt.Sscanf(strings.TrimPrefix(line, "version = "), "%d", &lf.FormatVersion)
			continue
		}
		if current == nil {
			continue
		}
		if value, ok := quotedValue(line, "name"); ok {
			current.Name = value
		} else if value, ok := quotedValue(line, "version"); ok {
			current.Version = value
		} else if value, ok := quotedValue(line, "source"); ok {
			current.Source = value
		}
	}
	flushPackage()
	if err := scanner.Err(); err != nil {
		return Lockfile{}, fmt.Errorf("error parsing lockfile: %w", err)
	}
	return lf, nil
}

func quotedValue(line, key string) (string, bool) {
	value, ok := strings.CutPrefix(line, key+" = ")
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	unquoted, err := strconv.Unquote(value)
	return unquoted, err == nil
}

// Parse extracts package information from Cargo.lock content.
func Parse(content string) ([]Package, error) {
	lf, err := ParseLockfile(content)
	return lf.Packages, err
}
