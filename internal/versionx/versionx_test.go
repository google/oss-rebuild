// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package versionx

import "testing"

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestApproxCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "2.0.0", 0},
		{"2.1.0", "2.0.9", 1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"2026.4.22", "2026.12.1", -1},
		{"1.5", "1.10", -1},
		{"0.10.0", "0.9.5", 1},
		{"1.0.0.post1", "1.0.0", 1},
		{"1.4.1", "1.17.0", -1},
		{"2.9.0.post0", "2.9.0", 1},
		// Spec agreements documented in the package comment.
		{"1.0a2", "1.0a12", -1},                   // PEP 440: within-stage ordering
		{"1.0.post456", "1.0.15", -1},             // PEP 440: digit run after letter run
		{"1.0", "1.0+deb1", -1},                   // Debian: additions order after
		{"1.0~beta1", "1.0~rc1", -1},              // Debian: ordering among tilde forms
		{"1-milestone", "1-rc", -1},               // Maven: qualifier order is alphabetical
		{"1-rc", "1-snapshot", -1},                // Maven: qualifier order is alphabetical
		{"1-foo2", "1-foo10", -1},                 // Maven: numeric qualifier suffixes
		{"1.K", "1.7", -1},                        // Maven: digit run after letter run
		{"1", "1-sp", -1},                         // Maven: sp orders after release
		{"1.0.a.2", "1.0.b1", -1},                 // Gem::Version: ordering among prereleases
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, // semver: strict path
		// Spec divergences documented in the package comment: each scheme's
		// pre-release marker orders before its base, Compare orders it after.
		{"1.0rc1", "1.0", 1},     // PEP 440 orders rc before the release
		{"1.0.dev456", "1.0", 1}, // PEP 440 orders dev before the release
		{"1.0~rc1", "1.0", 1},    // Debian orders ~ before the release
		{"1-snapshot", "1", 1},   // Maven orders snapshot before the release
		{"1.0.b1", "1.0", 1},     // Gem::Version orders prereleases before the release
	}
	for _, tc := range tests {
		got := ApproxCompare(tc.a, tc.b)
		if sign(got) != tc.want {
			t.Errorf("ApproxCompare(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}
