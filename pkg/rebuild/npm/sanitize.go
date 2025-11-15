// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// NPM Package Identifier Constraints
//
// NPM uses two formats:
//   - Scoped packages: "@scope/package-name" (e.g., "@fortawesome/react-fontawesome")
//   - Unscoped packages: "package-name" (e.g., "lodash")
//
// Character Constraints:
//   - Allowed characters: lowercase letters, digits, hyphens (-), underscores (_), periods (.)
//   - Special characters:
//     - '@' - Scope prefix (for scoped packages)
//     - '/' - Separator between scope and package name
//   - Reference: https://docs.npmjs.com/cli/v10/configuring-npm/package-json#name
//
// Firestore Encoding:
//   - Replaces '/' with '!' (exclamation mark)
//   - Example: "@fortawesome/react-fontawesome" → "@fortawesome!react-fontawesome"
//   - Note: '@' is allowed in Firestore document IDs, so it's kept as-is
//
// Filesystem/GCS Encoding:
//   - TODO: Breaks backward compatibility
//   - Will use same encoding as Firestore when implemented

// firestoreEncoder encodes NPM package identifiers for Firestore document IDs.
// Replaces forward slash with exclamation mark since it's forbidden in Firestore.
var firestoreEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '!'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

func init() {
	rebuild.RegisterEncoder(rebuild.NPM, rebuild.FirestoreTargetEncoding, firestoreEncoder)
}
