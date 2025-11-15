// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Debian Package Identifier Constraints and Encoding
//
// Debian itself supports two package formats (we currently require the former):
//   - With archive area: "archive/package" (e.g., "main/gcc-12", "contrib/nvidia-driver")
//   - Without archive area: "package" (e.g., "libc6", "python3.11", "g++")
//
// Archive areas include: main, contrib, non-free, non-free-firmware
// Reference: https://www.debian.org/doc/debian-policy/ch-archive.html
//
// Character Constraints:
//   - Allowed characters: lowercase letters, digits, hyphens (-), periods (.), plus signs (+)
//   - Special characters:
//     - '/' - Separator between archive area and package name
//   - Must start with lowercase letter or digit
//   - Must be at least 2 characters long
//   - Reference: https://www.debian.org/doc/debian-policy/ch-controlfields.html#s-f-source
//
// Filesystem/GCS Encoding:
//   - Replaces '/' with '~' (tilde)
//   - Example: "main/gcc-12" → "main~gcc-12"
//
// Firestore Encoding:
//   - Replaces '/' with '!' (exclamation mark)
//   - Example: "main/gcc-12" → "main!gcc-12"

var filesystemEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '~'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

var firestoreEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '!'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

func init() {
	rebuild.RegisterEncoder(rebuild.Debian, rebuild.FilesystemTargetEncoding, filesystemEncoder)
	rebuild.RegisterEncoder(rebuild.Debian, rebuild.FirestoreTargetEncoding, firestoreEncoder)
}
