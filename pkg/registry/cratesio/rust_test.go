// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"testing"
	"time"
)

func TestVersionAt(t *testing.T) {
	testCases := []struct {
		name        string
		inputDate   time.Time
		expected    string
		shouldError bool
	}{
		{"exact match", time.Date(2023, 9, 19, 0, 0, 0, 0, time.UTC), "1.72.1", false},
		{"between releases", time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), "1.72.1", false},
		{"before all releases", time.Date(2012, 10, 1, 0, 0, 0, 0, time.UTC), "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			version, err := RustVersionAt(tc.inputDate)

			if tc.shouldError {
				if err == nil {
					t.Error("Expected an error, but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if version != tc.expected {
					t.Errorf("Expected version %s, got %s", tc.expected, version)
				}
			}
		})
	}
}
