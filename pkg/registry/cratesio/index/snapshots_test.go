// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package index

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSnapshotList(t *testing.T) {
	ctx := t.Context()
	var calls int
	var fail bool
	l := &SnapshotList{TTL: time.Hour, List: func(context.Context) ([]string, error) {
		calls++
		if fail {
			return nil, errors.New("ls-remote failed")
		}
		return []string{"2025-06-14", "2025-09-05"}, nil
	}}
	want := []string{"2025-06-14", "2025-09-05"}
	for range 3 {
		got, err := l.Get(ctx)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("Get() = %v, %v; want %v", got, err, want)
		}
	}
	if calls != 1 {
		t.Errorf("listed %d times within the TTL, want 1", calls)
	}
	// Past the TTL a failed refresh serves the last listing.
	l.listedAt = time.Now().Add(-2 * time.Hour)
	fail = true
	if got, err := l.Get(ctx); err != nil || !slices.Equal(got, want) {
		t.Errorf("Get() after a failed refresh = %v, %v; want %v", got, err, want)
	}
	if calls != 2 {
		t.Errorf("listed %d times, want a refresh attempt past the TTL", calls)
	}
	// and holds off the next attempt for another TTL.
	if _, err := l.Get(ctx); err != nil {
		t.Errorf("Get() after a failed refresh = %v", err)
	}
	if calls != 2 {
		t.Errorf("listed %d times, want no retry within a TTL of a failed refresh", calls)
	}
	// With no listing yet, the failure surfaces.
	if _, err := (&SnapshotList{TTL: time.Hour, List: l.List}).Get(ctx); err == nil {
		t.Error("Get() with no listing and a failing lister succeeded")
	}
}
