// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cargolock

import (
	"bufio"
	"fmt"
	"strings"
)

// Package represents a crate dependency from a Cargo.lock file.
type Package struct {
	Name    string
	Version string
}

// Lockfile represents a parsed Cargo.lock file.
type Lockfile struct {
	// FormatVersion is the top-level `version` field of the lock file.
	// 0 means the version header was absent (implying format v1).
	FormatVersion int
	Packages      []Package
}

// ParseLockfile parses a Cargo.lock file, returning the format version and packages.
func ParseLockfile(content string) (Lockfile, error) {
	var lf Lockfile
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		// Top-level format version: `version = N` (unquoted integer, unlike package versions).
		if strings.HasPrefix(line, "version = ") && !strings.Contains(line, "\"") {
			fmt.Sscanf(strings.TrimPrefix(line, "version = "), "%d", &lf.FormatVersion)
			continue
		}
		// Package entry: `name = "foo"` immediately followed by `version = "x.y.z"`.
		if strings.HasPrefix(line, "name = ") {
			name := strings.Trim(strings.TrimPrefix(line, "name = "), "\"")
			if scanner.Scan() {
				versionLine := scanner.Text()
				if strings.HasPrefix(versionLine, "version = ") {
					version := strings.Trim(strings.TrimPrefix(versionLine, "version = "), "\"")
					lf.Packages = append(lf.Packages, Package{Name: name, Version: version})
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Lockfile{}, fmt.Errorf("error parsing lockfile: %w", err)
	}
	return lf, nil
}

// Parse extracts package information from Cargo.lock content.
func Parse(content string) ([]Package, error) {
	lf, err := ParseLockfile(content)
	return lf.Packages, err
}
