// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func durPtr(d time.Duration) *time.Duration { return &d }

func TestBuildTimingsPartialRoundTrip(t *testing.T) {
	in := BuildTimings{Setup: durPtr(30 * time.Second), Source: durPtr(45 * time.Second), FailedIn: PhaseSource}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got BuildTimings
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if diff := cmp.Diff(in, got); diff != "" {
		t.Errorf("round-trip diff (-want +got):\n%s", diff)
	}
}
