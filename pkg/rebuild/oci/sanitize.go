// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// OCI Identifier Constraints and Encoding
//
// OCI images names can contain slashes (e.g. "docker.io/library/debian").
//
// Filesystem/GCS Encoding:
//   - Replaces '/' with '!' (exclamation mark)

var filesystemEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '!'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

func init() {
	rebuild.RegisterEncoder(rebuild.OCI, rebuild.FilesystemTargetEncoding, filesystemEncoder)
}
