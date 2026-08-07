// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesregistryservice

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/registry/cratesio/index"
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

func TestFindRegistryCommitRequestTargetPair(t *testing.T) {
	for _, test := range []struct {
		name    string
		pkg     string
		version string
		wantErr bool
	}{
		{name: "both omitted"},
		{name: "both provided", pkg: "serde", version: "1.0.228"},
		{name: "package only", pkg: "serde", wantErr: true},
		{name: "version only", version: "1.0.228", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := FindRegistryCommitRequest{
				LockfileBase64: "Cg==",
				PublishedTime:  "2025-01-01T00:00:00Z",
				Package:        test.pkg,
				Version:        test.version,
			}
			if err := req.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestRepositoryKeysForPublication(t *testing.T) {
	published := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	snapshots := []string{"2026-05-25"}
	for _, test := range []struct {
		name        string
		targetAware bool
		want        []index.RepositoryKey
	}{
		{
			name:        "target anchored",
			targetAware: true,
			want:        []index.RepositoryKey{{Type: index.SnapshotIndex, Name: "2026-05-25"}},
		},
		{
			name: "legacy request",
			want: []index.RepositoryKey{
				{Type: index.CurrentIndex},
				{Type: index.SnapshotIndex, Name: "2026-05-25"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := repositoryKeysForPublication(snapshots, published, test.targetAware)
			if !slices.Equal(got, test.want) {
				t.Fatalf("repositoryKeysForPublication() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNextNewerRepositoryKey(t *testing.T) {
	snapshots := []string{"2026-04-19", "2026-04-28", "2026-05-25"}
	for _, test := range []struct {
		name string
		key  index.RepositoryKey
		want index.RepositoryKey
		ok   bool
	}{
		{
			name: "next snapshot",
			key:  index.RepositoryKey{Type: index.SnapshotIndex, Name: "2026-04-28"},
			want: index.RepositoryKey{Type: index.SnapshotIndex, Name: "2026-05-25"},
			ok:   true,
		},
		{
			name: "current after latest snapshot",
			key:  index.RepositoryKey{Type: index.SnapshotIndex, Name: "2026-05-25"},
			want: index.RepositoryKey{Type: index.CurrentIndex},
			ok:   true,
		},
		{name: "already current", key: index.RepositoryKey{Type: index.CurrentIndex}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := nextNewerRepositoryKey(test.key, snapshots)
			if ok != test.ok || got != test.want {
				t.Fatalf("nextNewerRepositoryKey() = (%v, %t), want (%v, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}
