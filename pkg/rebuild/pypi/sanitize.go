// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

// PyPI Package Identifier Constraints
//
// PyPI uses a single package name format with normalization rules:
//   - Package names like "absl-py", "requests", "Django"
//   - Case-insensitive: "Django" and "django" refer to the same package
//
// Character Constraints:
//   - Allowed characters: letters, digits, hyphens (-), underscores (_), periods (.)
//   - Normalization: hyphens, underscores, and periods are all treated as equivalent
//     - Example: "foo-bar", "foo_bar", and "foo.bar" are the same package
//   - Reference: https://peps.python.org/pep-0508/#names
//   - Official normalization: https://packaging.python.org/en/latest/specifications/name-normalization/
//
// No encoding needed:
//   - All allowed characters (letters, digits, hyphens, underscores, periods) are safe
