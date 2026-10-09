// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		pkg  string
		want string
	}{
		{
			pkg:  "github.com/foo/bar",
			want: "github.com!foo!bar",
		},
		{
			pkg:  "docker.io/library/debian",
			want: "docker.io!library!debian",
		},
	}

	for _, tt := range tests {
		target := rebuild.Target{
			Ecosystem: rebuild.OCI,
			Package:   tt.pkg,
		}

		if got := rebuild.FilesystemTargetEncoding.Encode(target).Package; got != tt.want {
			t.Errorf("FilesystemTargetEncoding.Encode(%q) = %q, want %q", tt.pkg, got, tt.want)
		}
		if got := rebuild.FirestoreTargetEncoding.Encode(target).Package; got != tt.want {
			t.Errorf("FirestoreTargetEncoding.Encode(%q) = %q, want %q", tt.pkg, got, tt.want)
		}
	}
}
