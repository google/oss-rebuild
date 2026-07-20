// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"testing"
)

func TestDetectRustVersionBounds(t *testing.T) {
	tests := []struct {
		name      string
		cargoToml string
		wantLo    string
		wantHi    string
	}{
		{
			name:      "Empty TOML",
			cargoToml: ``,
			wantLo:    "",
			wantHi:    "1.54.0", // No modern header; resolver absence is not evidence
		},
		{
			name: "Edition 2021 Without Resolver",
			cargoToml: `# See more keys and their definitions at https://doc.rust-lang.org/cargo/reference/manifest.html
#
# Note that this is the newer format, specifying edition explicitly.
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
edition = "2021"
name = "my-crate"
`,
			wantLo: "1.55.0", // Cargo 1.56-1.63 can emit this shape without a resolver
			wantHi: "",       // hi remains "999" and gets cleared
		},
		{
			name: "Old Header Without Resolver",
			cargoToml: `# See more keys and their definitions at https://doc.rust-lang.org/cargo/reference/manifest.html
[package]
edition = "2018"
name = "my-crate"
`,
			wantLo: "",
			wantHi: "1.54.0", // Cargo 1.51-1.54 can emit this shape without a resolver
		},
		{
			name: "Resolver Two (Old Header)",
			cargoToml: `
[package]
name = "my-crate"

[workspace]
resolver = "2"
`,
			wantLo: "1.51.0", // resolver=2 sets lo=1.51
			wantHi: "1.54.0", // No modern header sets hi=1.54
		},
		{
			name: "Resolver Two (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"

[workspace]
resolver = '2'
`,
			wantLo: "1.55.0", // modernHeader=1.55, resolver=2=1.51. Max is 1.55
			wantHi: "",
		},
		{
			name: "Package Resolver Two Has No Upper Bound",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
resolver = "2"
`,
			wantLo: "1.55.0", // modernHeader=1.55, resolver=2=1.51. Max is 1.55
			wantHi: "",       // resolver=2 is still retained after Cargo 1.63
		},
		{
			name: "Package Resolver Two With Trailing Comment",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
resolver = "2" # Explicitly retain resolver 2.
`,
			wantLo: "1.55.0",
			wantHi: "",
		},
		{
			name: "Resolver in Metadata Is Not Evidence",
			cargoToml: `
[package]
name = "my-crate"

[package.metadata.commands]
resolver = "2"
`,
			wantLo: "",
			wantHi: "1.54.0", // No modern header sets hi=1.54
		},
		{
			name: "Resolver in Multiline String Is Not Evidence",
			cargoToml: `
[package]
name = "my-crate"
description = """
resolver = "2"
"""
`,
			wantLo: "",
			wantHi: "1.54.0", // No modern header sets hi=1.54
		},
		{
			name: "Pretty Array (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
features = [
    "feature-a",
    "feature-b",
]
`,
			wantLo: "1.60.0", // prettyArray=1.60, modernHeader=1.55. Max is 1.60
			wantHi: "",
		},
		{
			name: "Doc Scrape Examples (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
doc-scrape-examples = true
`,
			wantLo: "1.67.0", // docExamples=1.67, modernHeader=1.55. Max is 1.67
			wantHi: "",
		},
		{
			name: "Debug Denormalized (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[profile.release]
debug = true
`,
			wantLo: "1.55.0", // modernHeader sets lo=1.55
			wantHi: "1.70.0", // debugDenormalized sets hi=1.70.0
		},
		{
			name: "All Lo Bounds (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
doc-scrape-examples = false
features = [
    "feature-a",
]
`,
			wantLo: "1.67.0", // docExamples=1.67, prettyArray=1.60, modernHeader=1.55. Max is 1.67
			wantHi: "",
		},
		{
			name: "All Hi Bounds (Old Header)",
			cargoToml: `
[package]
name = "my-crate"

[workspace]
resolver = "2"

[profile.dev]
debug = false
`,
			wantLo: "1.51.0", // resolver=2 sets lo=1.51
			wantHi: "1.54.0", // No modern header sets hi=1.54
		},
		{
			name: "All Hi Bounds (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"

[workspace]
resolver = "2"

[profile.dev]
debug = false
`,
			wantLo: "1.55.0", // modernHeader=1.55, resolver=2=1.51. Max is 1.55
			wantHi: "1.70.0", // debugDenormalized=1.70
		},
		{
			name: "All Bounds (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
doc-scrape-examples = true
features = [
    "feature-a",
]

[workspace]
resolver = "2"

[profile.dev]
debug = false
`,
			wantLo: "1.67.0", // docExamples=1.67, prettyArray=1.60, modernHeader=1.55, resolver=2=1.51. Max is 1.67
			wantHi: "1.70.0", // debugDenormalized=1.70
		},
		{
			name: "aho-corasick 1.0.4 (debug = 2, pretty arrays)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
edition = "2021"
rust-version = "1.60.0"
name = "aho-corasick"
version = "1.0.4"
keywords = [
    "string",
    "search",
]
categories = ["text-processing"]

[profile.release]
debug = 2
`,
			wantLo: "1.60.0", // prettyArray=1.60, modernHeader=1.55. Max is 1.60
			wantHi: "",
		},
		{
			name: "syn 2.0.39 (doc-scrape-examples = false)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
edition = "2021"
rust-version = "1.56"
name = "syn"
version = "2.0.39"
keywords = [
    "macros",
    "syn",
]
categories = [
    "development-tools::procedural-macro-helpers",
    "parser-implementations",
]

[lib]
doc-scrape-examples = false
`,
			wantLo: "1.67.0", // docExamples=1.67, prettyArray=1.60, modernHeader=1.55. Max is 1.67
			wantHi: "",
		},
		{
			name: "async-native-tls 0.4.0 (cuddled categories)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
edition = "2018"
name = "async-native-tls"
version = "0.4.0"
categories = ["asynchronous", "cryptography", "network-programming"]
`,
			wantLo: "1.55.0", // modernHeader=1.55, cuddledArray (hi=1.59) prevents bump to 1.64
			wantHi: "1.59.0", // cuddledArray=1.59
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLo, gotHi := detectRustVersionBounds(tt.cargoToml)
			if gotLo != tt.wantLo {
				t.Errorf("detectRustVersionBounds() gotLo = %v, want %v", gotLo, tt.wantLo)
			}
			if gotHi != tt.wantHi {
				t.Errorf("detectRustVersionBounds() gotHi = %v, want %v", gotHi, tt.wantHi)
			}
		})
	}
}
