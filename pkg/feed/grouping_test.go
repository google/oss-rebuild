// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestGroupForSmoketest(t *testing.T) {
	runID := "test-run"

	tests := []struct {
		name    string
		targets []rebuild.Target
		want    []schema.SmoketestRequest
	}{
		{
			name:    "Empty targets",
			targets: []rebuild.Target{},
			want:    nil,
		},
		{
			name: "Single target",
			targets: []rebuild.Target{
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3"},
			},
			want: []schema.SmoketestRequest{
				{Ecosystem: rebuild.NPM, Package: "lodash", Versions: []string{"1.2.3"}, ID: runID},
			},
		},
		{
			name: "Multiple targets, same package",
			targets: []rebuild.Target{
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3"},
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.4"},
			},
			want: []schema.SmoketestRequest{
				{Ecosystem: rebuild.NPM, Package: "lodash", Versions: []string{"1.2.3", "1.2.4"}, ID: runID},
			},
		},
		{
			name: "Multiple targets, different packages, same ecosystem",
			targets: []rebuild.Target{
				{Ecosystem: rebuild.PyPI, Package: "absl-py", Version: "1.2.3"},
				{Ecosystem: rebuild.PyPI, Package: "requests", Version: "2.28.1"},
			},
			want: []schema.SmoketestRequest{
				{Ecosystem: rebuild.PyPI, Package: "absl-py", Versions: []string{"1.2.3"}, ID: runID},
				{Ecosystem: rebuild.PyPI, Package: "requests", Versions: []string{"2.28.1"}, ID: runID},
			},
		},
		{
			name: "Multiple targets, different ecosystems",
			targets: []rebuild.Target{
				{Ecosystem: rebuild.PyPI, Package: "absl-py", Version: "1.2.3"},
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "4.17.21"},
			},
			want: []schema.SmoketestRequest{
				{Ecosystem: rebuild.NPM, Package: "lodash", Versions: []string{"4.17.21"}, ID: runID},
				{Ecosystem: rebuild.PyPI, Package: "absl-py", Versions: []string{"1.2.3"}, ID: runID},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupForSmoketest(tt.targets, runID)
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("GroupForSmoketest() diff (-got +want):\n%s", diff)
			}
		})
	}
}

func TestGroupForAttest(t *testing.T) {
	runID := "test-run"

	tests := []struct {
		name    string
		targets []rebuild.Target
		want    []schema.RebuildPackageRequest
	}{
		{
			name:    "Empty targets",
			targets: []rebuild.Target{},
			want:    nil,
		},
		{
			name:    "Single target (NPM)",
			targets: []rebuild.Target{{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3", Artifact: "lodash-1.2.3.tgz"}},
			want:    []schema.RebuildPackageRequest{{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3", Artifact: "lodash-1.2.3.tgz", ID: runID}},
		},
		{
			name: "Multiple targets, same package",
			targets: []rebuild.Target{
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3"},
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.4"},
			},
			want: []schema.RebuildPackageRequest{
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.3", ID: runID},
				{Ecosystem: rebuild.NPM, Package: "lodash", Version: "1.2.4", ID: runID},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupForAttest(tt.targets, runID)
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("GroupForAttest() diff (-got +want):\n%s", diff)
			}
		})
	}
}
