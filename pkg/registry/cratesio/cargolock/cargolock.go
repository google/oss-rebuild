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

// Parse extracts package information from Cargo.lock content.
func Parse(content string) ([]Package, error) {
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
					packages = append(packages, Package{Name: name, Version: version})
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error parsing lockfile: %w", err)
	}
	return packages, nil
}
