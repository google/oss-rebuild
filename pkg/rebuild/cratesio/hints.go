// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"regexp"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pelletier/go-toml/v2"
)

func semverMax(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || semver.Cmp(a, b) >= 0 {
		return a
	}
	return b
}

func semverMin(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || semver.Cmp(a, b) <= 0 {
		return a
	}
	return b
}

// Compiled regex patterns for detecting Rust version requirements
// TODO: These should be declarative and paired with semver comparisons.
var (
	// debugDenormalizedRegex detects debug = bool (Rust 1.71+ normalized boolean debug to integer)
	debugDenormalizedRegex = regexp.MustCompile(`(?m)^\s*debug\s*=\s*(true|false)\s*$`)
	// prettyArrayPattern detects pretty-printed arrays (Rust 1.60+)
	prettyArrayPattern = regexp.MustCompile(`(?s)\[\s*\n\s+.*\n\s*\]`)
	// inlineArrayLine matches array values that Cargo kept on one line.
	inlineArrayLine = regexp.MustCompile(`(?m)^\s*[\w-]+\s*=\s*(\[[^\n\[\]]*\])\s*$`)
	// modernHeaderPattern detects a change in the Cargo.toml header (Rust 1.55+)
	modernHeaderPattern = regexp.MustCompile(`#.*to registry \(e\.g\., crates\.io\) dependencies\.`)
	// docExamplesRegex detects the addition of the scrape indicator (Rust 1.67+)
	docExamplesRegex = regexp.MustCompile(`(?m)^\s*doc-scrape-examples\s*=\s*(true|false)\s*$`)
	// autoTargetsRegex matches the switch cargo package writes when it inlines
	// the discovered targets, since Cargo 1.80 (rust-lang/cargo#13713).
	autoTargetsRegex = regexp.MustCompile(`(?m)^\s*autobins\s*=\s*false\s*$`)
	// autoLibRegex matches the switch that form gained in Cargo 1.83 (rust-lang/cargo#14591).
	autoLibRegex = regexp.MustCompile(`(?m)^\s*autolib\s*=\s*false\s*$`)
	// resolverOneRegex matches resolver = "1" written by Cargo 1.64+ (#10911/#10961, backported #10970)
	// when workspace resolver differs from edition-2021 default.
	resolverOneRegex = regexp.MustCompile(`(?m)^\s*resolver\s*=\s*["']1["']\s*$`)
	// resolverTwoRegex matches resolver = "2" in Cargo.toml.
	resolverTwoRegex = regexp.MustCompile(`(?m)^\s*resolver\s*=\s*["']2["']\s*$`)
	// featuresSectionRegex and targetOrDepSectionRegex detect section ordering in Cargo.toml.
	featuresSectionRegex    = regexp.MustCompile(`(?m)^\[features\]\r?$`)
	targetOrDepSectionRegex = regexp.MustCompile(`(?m)^(\[(lib|(dev-|build-)?dependencies)(\.|\])|\[\[(bin|example|test|bench)\]\])`)
)

func packageEditionFloor(cargoTomlText string) string {
	var manifest struct {
		Package struct {
			Edition string `toml:"edition"`
		} `toml:"package"`
	}
	if toml.Unmarshal([]byte(cargoTomlText), &manifest) != nil {
		return ""
	}
	switch manifest.Package.Edition {
	case "2015", "2018":
		return "1.30.0"
	case "2021":
		return "1.56.0"
	case "2024":
		return "1.85.0"
	default:
		return ""
	}
}

// hasInlineMultiElementArray reports whether Cargo.toml contains an inline
// array with at least two top-level elements.
func hasInlineMultiElementArray(cargoTomlText string) bool {
	for _, m := range inlineArrayLine.FindAllStringSubmatch(cargoTomlText, -1) {
		var value struct {
			Array []any `toml:"array"`
		}
		if err := toml.Unmarshal([]byte("array = "+m[1]), &value); err == nil && len(value.Array) >= 2 {
			return true
		}
	}
	return false
}

// hasResolverTwo reports whether resolver 2 is set in a supported manifest section.
func hasResolverTwo(cargoTomlText string) bool {
	var manifest struct {
		Package struct {
			Resolver string `toml:"resolver"`
		} `toml:"package"`
		Workspace struct {
			Resolver string `toml:"resolver"`
		} `toml:"workspace"`
	}
	return toml.Unmarshal([]byte(cargoTomlText), &manifest) == nil &&
		(manifest.Package.Resolver == "2" || manifest.Workspace.Resolver == "2")
}

// cargoWrote reports whether re matches the published manifest but not the
// author's original, which only the packaging cargo produces.
func cargoWrote(re *regexp.Regexp, published, orig string) bool {
	return orig != "" && re.MatchString(published) && !re.MatchString(orig)
}

// detectRustVersionBounds analyzes Cargo.toml for structural patterns that indicate
// minimum Rust version requirements based on tooling behavior changes. origText
// is the manifest as the author wrote it (Cargo.toml.orig), empty when absent.
func detectRustVersionBounds(cargoTomlText, origText string) (lo, hi string) {
	editionFloor := packageEditionFloor(cargoTomlText)
	// Check patterns from latest to earliest Rust version
	if cargoWrote(autoLibRegex, cargoTomlText, origText) {
		lo = semverMax("1.83.0", lo)
	}
	if cargoWrote(autoTargetsRegex, cargoTomlText, origText) {
		lo = semverMax("1.80.0", lo)
	}
	if cargoWrote(resolverOneRegex, cargoTomlText, origText) {
		lo = semverMax("1.64.0", lo)
	} else if editionFloor == "1.56.0" && cargoWrote(resolverTwoRegex, cargoTomlText, origText) {
		// Cargo 1.56..1.63 wrote redundant resolver = "2" on edition = "2021" crates (#10961).
		lo = semverMax("1.56.0", lo)
		hi = semverMin("1.63.0", hi)
	}
	// Cargo 1.85 (#14931) moved [features] before target tables ([lib], [[bin]], etc.) and [*dependencies].
	if origText != "" {
		if featIdx := featuresSectionRegex.FindStringIndex(cargoTomlText); featIdx != nil {
			if otherIdx := targetOrDepSectionRegex.FindStringIndex(cargoTomlText); otherIdx != nil {
				if featIdx[0] < otherIdx[0] {
					lo = semverMax("1.85.0", lo)
				} else {
					hi = semverMin("1.84.1", hi)
				}
			}
		}
	}
	if debugDenormalizedRegex.MatchString(cargoTomlText) {
		hi = semverMin("1.70.0", hi) // After which bools were normalized to ints
	}
	if docExamplesRegex.MatchString(cargoTomlText) {
		lo = semverMax("1.67.0", lo) // Before which the property was omitted
	}
	if prettyArrayPattern.MatchString(cargoTomlText) {
		lo = semverMax("1.60.0", lo) // Before which arrays were cuddled
	} else if hasInlineMultiElementArray(cargoTomlText) {
		hi = semverMin("1.59.0", hi) // After which multi-element arrays were pretty-printed
	}
	if modernHeaderPattern.MatchString(cargoTomlText) {
		lo = semverMax("1.55.0", lo)
	} else {
		hi = semverMin("1.54.0", hi)
	}
	if hasResolverTwo(cargoTomlText) {
		lo = semverMax("1.51.0", lo)
	}
	if editionFloor != "" {
		lo = semverMax(editionFloor, lo)
	}
	if hi != "" && lo != "" && semver.Cmp(hi, lo) < 0 {
		hi = ""
	}
	return
}
