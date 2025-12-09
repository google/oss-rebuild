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
			wantHi:    "1.50.0", // No modern header, no resolver=2
		},
		{
			name: "Modern Header Only",
			cargoToml: `# See more keys and their definitions at https://doc.rust-lang.org/cargo/reference/manifest.html
#
# Note that this is the newer format, specifying edition explicitly.
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[package]
name = "my-crate"
`,
			wantLo: "1.64.0", // modernHeader sets lo=1.55, no resolver=2 bumps to 1.64
			wantHi: "",       // hi remains "999" and gets cleared
		},
		{
			name: "Old Header Only",
			cargoToml: `# See more keys and their definitions at https://doc.rust-lang.org/cargo/reference/manifest.html
[package]
name = "my-crate"
`,
			wantLo: "",
			wantHi: "1.50.0", // No modern header sets hi=1.54, no resolver=2 bumps to 1.50
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
			wantHi: "1.54.0", // No modern header sets hi=1.54, resolver=2 sets hi=1.63. min(1.54, 1.63) = 1.54
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
			wantLo: "1.55.0", // modernHeader sets lo=1.55, resolver=2 sets lo=1.51. max(1.55, 1.51) = 1.55
			wantHi: "1.63.0", // resolver=2 sets hi=1.63
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
			wantLo: "1.64.0", // prettyArray sets lo=1.60, modernHeader sets lo=1.55. max(1.60, 1.55) = 1.60. no resolver=2 bumps to 1.64
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
			wantLo: "1.67.0", // docExamples sets lo=1.67. modernHeader sets lo=1.55. max(1.67, 1.55) = 1.67. no resolver=2 bumps to max(1.64, 1.67) = 1.67
			wantHi: "",
		},
		{
			name: "Debug Denormalized (Modern Header)",
			cargoToml: `
# Before Rust 1.55, crates.io would automatically add this header to registry (e.g., crates.io) dependencies.
[profile.release]
debug = true
`,
			wantLo: "1.64.0", // modernHeader sets lo=1.55. no resolver=2 bumps to 1.64
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
			wantLo: "1.67.0", // docExamples=1.67, prettyArray=1.60, modernHeader=1.55. Max is 1.67. no resolver=2 bumps to max(1.64, 1.67) = 1.67
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
			wantHi: "1.54.0", // debugDenormalized=1.70, no modernHeader=1.54, resolver=2=1.63. Min is 1.54
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
			wantHi: "1.63.0", // debugDenormalized=1.70, resolver=2=1.63. Min is 1.63
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
			wantHi: "1.63.0", // debugDenormalized=1.70, resolver=2=1.63. Min is 1.63
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
			wantLo: "1.64.0", // prettyArray=1.60, modernHeader=1.55, no resolver=max(1.64,1.60)=1.64
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
			wantLo: "1.67.0", // docExamples=1.67, prettyArray=1.60, modernHeader=1.55. no resolver=max(1.64,1.67)=1.67
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
