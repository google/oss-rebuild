// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/semver"
)

func TestCibuildwheelPinTable_ImageAt(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2025, time.January, d, 0, 0, 0, 0, time.UTC) }
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	commit := func(c string) string { return strings.Repeat(c, 40) }
	pins := CibuildwheelPinTable{
		{
			Version:   "1.0.0",
			Published: day(1),
			Commit:    commit("1"),
			Images: map[string]PinnedImage{
				ImageManylinux2014X86_64: {Tag: "t1", Digest: digest("1")},
				ImageMusllinux1_1X86_64:  {Tag: "m1", Digest: digest("2")},
			},
		},
		{
			Version:   "1.1.0",
			Published: day(3),
			Commit:    commit("2"),
			Images: map[string]PinnedImage{
				ImageManylinux2014X86_64: {Tag: "expired"},
			},
		},
		{
			Version:   "2.0.0",
			Published: day(10),
			Commit:    commit("3"),
			Images: map[string]PinnedImage{
				ImageManylinux2014X86_64: {Tag: "t2", Digest: digest("3")},
				ImageMusllinux1_1X86_64:  {Tag: "m2", Digest: digest("4")},
			},
		},
		{
			Version:   "3.0.0",
			Published: day(20),
			Commit:    commit("4"),
			Images: map[string]PinnedImage{
				ImageManylinux2014X86_64: {Digest: digest("5")},
				ImageManylinux2_34X86_64: {Tag: "n3", Digest: digest("6")},
			},
		},
		{
			Version:   "2.0.1",
			Published: day(22),
			Commit:    commit("5"),
			Images: map[string]PinnedImage{
				ImageManylinux2014X86_64: {Tag: "t21", Digest: digest("7")},
				ImageMusllinux1_1X86_64:  {Tag: "m21", Digest: digest("8")},
			},
		},
	}
	type result struct {
		Ref, Version string
		OK           bool
	}
	tests := []struct {
		name  string
		repo  string
		t     time.Time
		specs []string
		want  result
	}{
		{
			name: "BeforeFirstRelease",
			repo: ImageManylinux2014X86_64,
			t:    day(0),
			want: result{},
		},
		{
			name: "ExpiredTagHasNoFallback",
			repo: ImageManylinux2014X86_64,
			t:    day(5),
			want: result{},
		},
		{
			name: "AtPublishTime",
			repo: ImageManylinux2014X86_64,
			t:    day(10),
			want: result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name: "BetweenReleases",
			repo: ImageManylinux2014X86_64,
			t:    day(15),
			want: result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name: "DigestOnlyPin",
			repo: ImageManylinux2014X86_64,
			t:    day(21),
			want: result{Ref: ImageManylinux2014X86_64 + "@" + digest("5"), Version: "3.0.0", OK: true},
		},
		{
			name: "MaintenanceReleaseOfOlderMajor",
			repo: ImageManylinux2014X86_64,
			t:    day(25),
			want: result{Ref: ImageManylinux2014X86_64 + "@" + digest("5"), Version: "3.0.0", OK: true},
		},
		{
			name: "DroppedRepoUsesHighestPinningVersion",
			repo: ImageMusllinux1_1X86_64,
			t:    day(25),
			want: result{Ref: ImageMusllinux1_1X86_64 + ":m21@" + digest("8"), Version: "2.0.1", OK: true},
		},
		{
			name: "RepoNotPinnedYet",
			repo: ImageManylinux2_34X86_64,
			t:    day(15),
			want: result{},
		},
		{
			name: "RepoNeverPinned",
			repo: ImageMusllinux1_2X86_64,
			t:    day(25),
			want: result{},
		},
		{
			name:  "ExactVersionSpec",
			repo:  ImageManylinux2014X86_64,
			t:     day(25),
			specs: []string{"2.0.0"},
			want:  result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name:  "CommitSpec",
			repo:  ImageManylinux2014X86_64,
			t:     day(25),
			specs: []string{commit("3")},
			want:  result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name:  "MinorSeriesSpecAtUploadTime",
			repo:  ImageManylinux2014X86_64,
			t:     day(15),
			specs: []string{"2.0"},
			want:  result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name:  "MinorSeriesSpecBeforeFirstRelease",
			repo:  ImageManylinux2014X86_64,
			t:     day(5),
			specs: []string{"2.0"},
			want:  result{},
		},
		{
			name:  "MinorSeriesSpecIncludesLaterPatch",
			repo:  ImageManylinux2014X86_64,
			t:     day(25),
			specs: []string{"2.0"},
			want:  result{Ref: ImageManylinux2014X86_64 + ":t21@" + digest("7"), Version: "2.0.1", OK: true},
		},
		{
			name:  "ExpiredSpecHasNoFallback",
			repo:  ImageManylinux2014X86_64,
			t:     day(25),
			specs: []string{"1.1.0"},
			want:  result{},
		},
		{
			name:  "MultipleSpecsPicksHighest",
			repo:  ImageManylinux2014X86_64,
			t:     day(25),
			specs: []string{"1.0.0", commit("3"), "untagged"},
			want:  result{Ref: ImageManylinux2014X86_64 + ":t2@" + digest("3"), Version: "2.0.0", OK: true},
		},
		{
			name:  "MultipleSpecsSkipsUnpinnedRepo",
			repo:  ImageMusllinux1_1X86_64,
			t:     day(25),
			specs: []string{"1.0.0", "3.0.0"},
			want:  result{Ref: ImageMusllinux1_1X86_64 + ":m1@" + digest("2"), Version: "1.0.0", OK: true},
		},
		{
			name:  "SpecWithoutRepoFails",
			repo:  ImageMusllinux1_1X86_64,
			t:     day(25),
			specs: []string{"3.0.0"},
			want:  result{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got result
			got.Ref, got.Version, got.OK = pins.ImageAt(tc.repo, tc.t, tc.specs...)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ImageAt(%q, %v, %v) returned diff (-want +got):\n%s", tc.repo, tc.t, tc.specs, diff)
			}
		})
	}
}

// TestCibuildwheelPins guards the lookups against generated versions that
// internal/semver cannot order and against ambiguous commits.
func TestCibuildwheelPins(t *testing.T) {
	commitPat := regexp.MustCompile(`^[0-9a-f]{40}$`)
	versions := make(map[string]bool)
	commits := make(map[string]bool)
	for _, rel := range CibuildwheelPins {
		if _, err := semver.New(rel.Version); err != nil {
			t.Errorf("version %q is not a semantic version", rel.Version)
		}
		if versions[rel.Version] {
			t.Errorf("version %q is listed more than once", rel.Version)
		}
		versions[rel.Version] = true
		if !commitPat.MatchString(rel.Commit) {
			t.Errorf("commit %q of %s is not a full commit hash", rel.Commit, rel.Version)
		}
		if commits[rel.Commit] {
			t.Errorf("commit %q of %s is listed more than once", rel.Commit, rel.Version)
		}
		commits[rel.Commit] = true
	}
}
