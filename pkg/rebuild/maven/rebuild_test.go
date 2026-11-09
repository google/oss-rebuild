// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestArtifactName(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkg  string
		want string
	}{
		{"GroupAndArtifact", "com.google.guava:guava", "guava-33.0.0.jar"},
		{"NoGroup", "guava", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ArtifactName(rebuild.Target{Ecosystem: rebuild.Maven, Package: tc.pkg, Version: "33.0.0"})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ArtifactName(%s) returned diff (-want +got):\n%s", tc.pkg, diff)
			}
		})
	}
}
