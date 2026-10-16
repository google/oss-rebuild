// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
	"time"
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

func TestGetRelevantSnapshots(t *testing.T) {
	snapshots := []string{
		"2022-03-02",
		"2022-07-02",
		"2022-10-16",
		"2023-03-10",
		"2023-08-19",
		"2024-02-17",
		"2024-08-17",
	}
	pub, err := time.Parse(time.RFC3339, "2024-05-01T12:00:00Z")
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	got := getRelevantSnapshots(snapshots, pub)
	want := []string{
		"2024-08-17",
		"2024-02-17",
		"2023-08-19",
		"2023-03-10",
	}
	if !slices.Equal(got, want) {
		t.Errorf("getRelevantSnapshots() = %v, want %v", got, want)
	}
}
