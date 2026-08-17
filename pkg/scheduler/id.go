// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"net/url"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Firestore document IDs here percent-escape each component and join with "!".
// Escaping is required because Firestore forbids "/" in a document ID and
// scoped npm names contain one. Joining with "!" rather than concatenating
// keeps the ID reversible and legible in the console.

// PackageID is the document ID for a package.
func PackageID(ecosystem, pkg string) string {
	return url.PathEscape(ecosystem) + "!" + url.PathEscape(pkg)
}

// TargetID is the document ID for a target. Components left empty still
// contribute a separator, so a package-level and a version-level ID for the
// same package never collide.
func TargetID(t rebuild.Target) string {
	return strings.Join([]string{
		url.PathEscape(string(t.Ecosystem)),
		url.PathEscape(t.Package),
		url.PathEscape(t.Version),
		url.PathEscape(t.Artifact),
	}, "!")
}
