// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestTargetIDEscapesSeparators(t *testing.T) {
	// Scoped npm names contain "/", which Firestore forbids in document IDs.
	id := TargetID(rebuild.Target{Ecosystem: rebuild.NPM, Package: "@scope/name", Version: "1.0.0", Artifact: "a.tgz"})
	if diff := cmp.Diff("npm!@scope%2Fname!1.0.0!a.tgz", id); diff != "" {
		t.Errorf("TargetID mismatch (-want +got):\n%s", diff)
	}
	// A name containing the separator must not produce another target's ID.
	split := rebuild.Target{Ecosystem: rebuild.NPM, Package: "a", Version: "b"}
	if TargetID(rebuild.Target{Ecosystem: rebuild.NPM, Package: "a!b"}) == TargetID(split) {
		t.Error("TargetID collides with its own separator")
	}
}
