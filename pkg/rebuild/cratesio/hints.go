// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"regexp"
)

// Compiled regex patterns for detecting Rust version requirements
// TODO: These should be declarative and paired with semver comparisons.
var (
	// debugDenormalizedRegex detects debug = bool (Rust 1.71+ normalized boolean debug to integer)
	debugDenormalizedRegex = regexp.MustCompile(`(?m)^\s*debug\s*=\s*(true|false)\s*$`)
	// resolverTwoPattern detects resolver = "2" (became default and was removed in Rust 1.64+)
	resolverTwoPattern = regexp.MustCompile(`(?m)^\s*resolver\s*=\s*["\']?2["\']?\s*$`)
	// prettyArrayPattern detects pretty-printed arrays (Rust 1.60+)
	prettyArrayPattern = regexp.MustCompile(`(?s)\[\s*\n\s+.*\n\s*\]`)
	// cuddledArrayPattern detects cuddled/single-line arrays (Rust < 1.60)
	cuddledArrayPattern = regexp.MustCompile(`(?m)^\s*\w+\s*=\s*\[[^\n\[\]]*\]`)
	// modernHeaderPattern detects a change in the Cargo.toml header (Rust 1.55+)
	modernHeaderPattern = regexp.MustCompile(`#.*to registry \(e\.g\., crates\.io\) dependencies\.`)
	// docExamplesRegex detects the addition of the scrape indicator (Rust 1.67+)
	docExamplesRegex = regexp.MustCompile(`(?m)^\s*doc-scrape-examples\s*=\s*(true|false)\s*$`)
)

// detectRustVersionBounds analyzes Cargo.toml for structural patterns that indicate
// minimum Rust version requirements based on tooling behavior changes.
func detectRustVersionBounds(cargoTomlText string) (lo, hi string) {
	hi = "999" // NOTE: Temporarily set "hi" so it will sort higher than all our candidates
	// Check patterns from latest to earliest Rust version
	if debugDenormalizedRegex.MatchString(cargoTomlText) {
		hi = "1.70.0" // After which bools were normalized to ints
	}
	if docExamplesRegex.MatchString(cargoTomlText) {
		lo = "1.67.0" // Before which the property was omitted
	}
	if prettyArrayPattern.MatchString(cargoTomlText) {
		lo = max("1.60.0", lo) // Before which arrays were cuddled
	} else if cuddledArrayPattern.MatchString(cargoTomlText) {
		hi = min("1.59.0", hi) // After which arrays were pretty-printed
	}
	if modernHeaderPattern.MatchString(cargoTomlText) {
		lo = max("1.55.0", lo)
	} else {
		hi = min("1.54.0", hi)
	}
	if resolverTwoPattern.MatchString(cargoTomlText) {
		hi = min("1.63.0", hi) // After which resolver 2 became default and was omitted
		lo = max("1.51.0", lo)
	} else {
		// If resolver pattern not found, we know the version lies outside the above range
		if lo > "1.51.0" {
			if hi >= "1.64.0" { // only raise lo if hi is in range
				lo = max("1.64.0", lo)
			}
		} else if hi < "1.63.0" {
			hi = min("1.50.0", hi)
		}
	}
	if hi == "999" {
		hi = ""
	}
	return
}
