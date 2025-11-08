// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

// Crates.io Package Identifier Constraints
//
// Crates.io uses a single package name format (called "crates"):
//   - Package names like "serde", "tokio", "rand_core"
//   - Lowercase alphanumeric with hyphens and underscores
//
// Character Constraints:
//   - Allowed characters: lowercase letters, digits, hyphens (-), underscores (_)
//   - Must start with a letter
//   - Maximum length: 64 characters
//   - Cannot be Rust keywords
//   - Reference: https://doc.rust-lang.org/cargo/reference/manifest.html#the-name-field
//   - Registry rules: https://crates.io/policies#package-naming
//
// No encoding needed:
//   - All allowed characters (lowercase letters, digits, hyphens, underscores) are safe
