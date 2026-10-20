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

// Compiled regex patterns for detecting Rust version requirements.
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

// crateProps holds the packaged crate properties used to bound the Rust
// toolchain version that produced the artifact.
type crateProps struct {
	CargoTOML       string
	OrigTOML        string
	LockfileVersion int
	DeclaredMSRV    string
}

type versionRule struct {
	lo, hi string
	when   func(p crateProps) bool
}

func hasManifest(re *regexp.Regexp) func(p crateProps) bool {
	return func(p crateProps) bool { return re.MatchString(p.CargoTOML) }
}

func cargoAdded(re *regexp.Regexp) func(p crateProps) bool {
	return func(p crateProps) bool { return cargoWrote(re, p.CargoTOML, p.OrigTOML) }
}

// featuresOrder returns -1 if [features] precedes target/dependency tables,
// +1 if it follows them, or 0 if both are not present.
func featuresOrder(cargoTomlText string) int {
	featIdx := featuresSectionRegex.FindStringIndex(cargoTomlText)
	otherIdx := targetOrDepSectionRegex.FindStringIndex(cargoTomlText)
	if featIdx == nil || otherIdx == nil {
		return 0
	}
	if featIdx[0] < otherIdx[0] {
		return -1
	}
	return 1
}

// versionRules bounds the Cargo version that packaged the crate.
var versionRules = []versionRule{
	// Cargo 1.85 (#14931) moved [features] before target and [*dependencies] tables.
	{lo: "1.85.0", when: func(p crateProps) bool { return p.OrigTOML != "" && featuresOrder(p.CargoTOML) < 0 }},
	{hi: "1.84.1", when: func(p crateProps) bool { return p.OrigTOML != "" && featuresOrder(p.CargoTOML) > 0 }},
	// Cargo 1.83 (#14591) began writing autolib = false.
	{lo: "1.83.0", when: cargoAdded(autoLibRegex)},
	// Cargo 1.80 (#13713) began writing autobins = false and inlining discovered targets.
	{lo: "1.80.0", when: cargoAdded(autoTargetsRegex)},
	// Cargo 1.78 stabilized Cargo.lock format v4.
	{lo: "1.78.0", when: func(p crateProps) bool { return p.LockfileVersion == 4 }},
	// Cargo 1.71 normalized boolean profile debug settings to integers.
	{hi: "1.70.0", when: hasManifest(debugDenormalizedRegex)},
	// Cargo 1.67 added doc-scrape-examples to target sections.
	{lo: "1.67.0", when: hasManifest(docExamplesRegex)},
	// Cargo 1.64 (#10911/#10970) began writing resolver = "1" when workspace resolver differs.
	{lo: "1.64.0", when: cargoAdded(resolverOneRegex)},
	// Cargo 1.56..1.63 wrote redundant resolver = "2" on edition = "2021" crates (#10961).
	{lo: "1.56.0", hi: "1.63.0", when: func(p crateProps) bool {
		return packageEditionFloor(p.CargoTOML) == "1.56.0" && cargoWrote(resolverTwoRegex, p.CargoTOML, p.OrigTOML)
	}},
	// Cargo 1.60 began multi-line pretty-printing multi-element arrays.
	{lo: "1.60.0", when: hasManifest(prettyArrayPattern)},
	{hi: "1.59.0", when: func(p crateProps) bool {
		return !prettyArrayPattern.MatchString(p.CargoTOML) && hasInlineMultiElementArray(p.CargoTOML)
	}},
	// Cargo 1.55 updated the generated Cargo.toml header comment.
	{lo: "1.55.0", when: hasManifest(modernHeaderPattern)},
	{hi: "1.54.0", when: func(p crateProps) bool { return !modernHeaderPattern.MatchString(p.CargoTOML) }},
	// Cargo 1.51 stabilized resolver = "2".
	{lo: "1.51.0", when: func(p crateProps) bool { return hasResolverTwo(p.CargoTOML) }},
	// Cargo 1.47 stabilized Cargo.lock format v3.
	{lo: "1.47.0", when: func(p crateProps) bool { return p.LockfileVersion == 3 }},
}

// inferRustVersionBounds analyzes packaged crate properties for structural
// patterns that bound the Rust toolchain version used to package the crate.
func inferRustVersionBounds(p crateProps) (lo, hi string) {
	lo = semverMax(p.DeclaredMSRV, packageEditionFloor(p.CargoTOML))
	for _, r := range versionRules {
		if r.when(p) {
			lo = semverMax(r.lo, lo)
			hi = semverMin(r.hi, hi)
		}
	}
	if hi != "" && lo != "" && semver.Cmp(hi, lo) < 0 {
		hi = ""
	}
	return lo, hi
}
