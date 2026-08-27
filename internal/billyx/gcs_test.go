// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"testing"

	"github.com/go-git/go-billy/v5"
)

// The GCS filesystem's network surface is only able to be exercised against
// real buckets so these tests only cover the credential-free logic: path
// resolution, rooting, and the declared capability subset.

func TestGCSChrootAndRoot(t *testing.T) {
	fs := NewGCS(t.Context(), nil, "bucket", "exports")
	if got := fs.Root(); got != "gs://bucket/exports" {
		t.Errorf("Root = %q", got)
	}
	sub, err := fs.Chroot("2026")
	if err != nil {
		t.Fatalf("Chroot: %v", err)
	}
	if got := sub.Root(); got != "gs://bucket/exports/2026" {
		t.Errorf("chrooted Root = %q", got)
	}
}

func TestGCSCapabilities(t *testing.T) {
	caps := billy.Capabilities(NewGCS(t.Context(), nil, "bucket", ""))
	if caps&billy.ReadCapability == 0 || caps&billy.WriteCapability == 0 {
		t.Errorf("capabilities = %b, want read+write", caps)
	}
	if caps&(billy.ReadAndWriteCapability|billy.SeekCapability|billy.TruncateCapability|billy.LockCapability) != 0 {
		t.Errorf("capabilities = %b, claims more than sequential read xor write", caps)
	}
}
