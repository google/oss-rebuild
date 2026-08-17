// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDocumentIDsEscapeSeparators(t *testing.T) {
	// Scoped npm names contain "/", which Firestore forbids in document IDs.
	if diff := cmp.Diff("npm!@babel%2Fcore", PackageID("npm", "@babel/core")); diff != "" {
		t.Errorf("PackageID mismatch (-want +got):\n%s", diff)
	}
	id := TargetID(rebuild.Target{Ecosystem: rebuild.NPM, Package: "@scope/name", Version: "1.0.0", Artifact: "a.tgz"})
	if strings.Contains(id, "/") {
		t.Errorf("TargetID must not contain '/': %q", id)
	}
	if got, want := strings.Count(id, "!"), 3; got != want {
		t.Errorf("TargetID has %d separators, want %d: %q", got, want, id)
	}
	// A name containing the separator must not produce another pair's ID.
	if PackageID("npm", "a!b") == PackageID("npm", "a")+"!b" {
		t.Error("PackageID collides with its own separator")
	}
}
