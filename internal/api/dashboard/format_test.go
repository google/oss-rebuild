// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"
	"time"
)

func TestGroupDigits(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{14889, "14,889"},
		{1234567, "1,234,567"},
		{-123, "-123"},
		{-1234, "-1,234"},
	} {
		if got := groupDigits(tc.n); got != tc.want {
			t.Errorf("groupDigits(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestSince(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"Seconds", 59 * time.Second, "just now"},
		{"Future", -time.Hour, "just now"},
		{"Minutes", 61 * time.Second, "1m ago"},
		{"Hours", 23*time.Hour + 59*time.Minute, "23h ago"},
		{"Days", 8*24*time.Hour + time.Hour, "8d ago"},
		{"PastAMonth", 30 * 24 * time.Hour, "2026-09-06"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := since(now, now.Add(-tc.ago)); got != tc.want {
				t.Errorf("since() = %q, want %q", got, tc.want)
			}
		})
	}
}
