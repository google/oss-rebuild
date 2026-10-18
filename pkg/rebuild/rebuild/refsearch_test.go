// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/pkg/errors"
)

func TestSearchWindow(t *testing.T) {
	if since, until := SearchWindow(time.Time{}); since != nil || until != nil {
		t.Errorf("SearchWindow(zero) = %v, %v, want both open", since, until)
	}
	published := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	since, until := SearchWindow(published)
	if since == nil || !since.Equal(published.Add(-searchLookback)) || until == nil || !until.Equal(published.Add(searchSlack)) {
		t.Errorf("SearchWindow(%v) = %v, %v, want lookback and slack around it", published, since, until)
	}
}

func TestErrorWithFallbackRef(t *testing.T) {
	repo, err := gitxtest.CreateRepoFromYAML(`commits:
  - id: old
    time: 2023-01-01T00:00:00Z
  - id: release
    parent: old
    time: 2023-05-30T00:00:00Z
  - id: later
    parent: release
    time: 2024-01-01T00:00:00Z
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := Location{Repo: "https://github.com/org/repo", Dir: "pkg"}
	for _, tc := range []struct {
		name      string
		published time.Time
		want      string
	}{
		{"LastCommitByPublishTime", time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC), "release"},
		{"CommitAfterPublishIsTooLate", time.Date(2023, 5, 29, 0, 0, 0, 0, time.UTC), "old"},
		{"NoPublishTimeUsesHEAD", time.Time{}, "later"},
		{"PublishBeforeEveryCommitUsesHEAD", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrorWithFallbackRef(ErrNoValidRef, repo.Repository, loc, tc.published)
			var ie *InferenceError
			if !errors.As(err, &ie) {
				t.Fatalf("ErrorWithFallbackRef() = %v, want an InferenceError", err)
			}
			want := InferenceErrorDetail{Location: Location{Repo: loc.Repo, Dir: loc.Dir, Ref: repo.Commits[tc.want].String()}, Published: tc.published, Kind: FailureNoRef}
			if diff := cmp.Diff(want, ie.Detail); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
			if !errors.Is(err, ErrNoValidRef) || !strings.HasSuffix(err.Error(), ": no git ref: all candidates invalid") {
				t.Errorf("ErrorWithFallbackRef() = %q, want an ErrNoValidRef that keeps its message", err)
			}
		})
	}
	var ie *InferenceError
	if err := ErrorWithFallbackRef(ErrNoRef, repo.Repository, loc, time.Time{}); !errors.As(err, &ie) || !errors.Is(err, ErrNoRef) {
		t.Errorf("ErrorWithFallbackRef(ErrNoRef) = %v, want an InferenceError that is an ErrNoRef", err)
	}
	other := errors.New("clone failed")
	if got := ErrorWithFallbackRef(other, repo.Repository, loc, time.Time{}); got != other {
		t.Errorf("ErrorWithFallbackRef(other) = %v, want it unchanged", got)
	}
}
