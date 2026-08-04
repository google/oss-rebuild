// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestFindRegistryCommitWithoutRegistryPackages(t *testing.T) {
	lockfile := `version = 3

[[package]]
name = "example"
version = "1.0.0"
`
	response, err := FindRegistryCommit(context.Background(), FindRegistryCommitRequest{
		LockfileBase64: base64.StdEncoding.EncodeToString([]byte(lockfile)),
		PublishedTime:  "2026-01-01T00:00:00Z",
	}, &FindRegistryCommitDeps{})
	if err != nil {
		t.Fatalf("FindRegistryCommit() error = %v", err)
	}
	if response.CommitHash != "" {
		t.Errorf("FindRegistryCommit() commit = %q, want empty", response.CommitHash)
	}
}
