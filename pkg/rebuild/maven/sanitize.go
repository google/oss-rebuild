// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Maven Package Identifier Constraints and Encoding
//
// Maven uses a colon-separated format where:
//   - groupId: Typically uses dot notation (e.g., "org.apache.commons")
//   - artifactId: The package name (e.g., "commons-lang3")
//   - Full identifier: "org.apache.commons:commons-lang3"
//
// Character Constraints:
//   - Allowed characters: letters, digits, dots (.), hyphens (-), underscores (_), colon (:)
//   - Special characters:
//     - ':' - Separator between groupId and artifactId
//     - '.' - Namespace separator within groupId
//   - Reference: https://maven.apache.org/guides/mini/guide-naming-conventions.html
//
// Filesystem/GCS Encoding:
//   - Replaces ':' with '~' (tilde)
//   - Example: "org.apache.commons:commons-lang3" → "org.apache.commons~commons-lang3"

var filesystemEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{':': '~'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

func init() {
	rebuild.RegisterEncoder(rebuild.Maven, rebuild.FilesystemTargetEncoding, filesystemEncoder)
}
