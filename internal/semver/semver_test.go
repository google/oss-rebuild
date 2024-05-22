// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package semver

import "testing"

func TestNew(t *testing.T) {
	tests := []struct {
		input    string
		expected Semver
		wantErr  bool
	}{
		{"1.2.3", Semver{1, 2, 3, "", ""}, false},                       // Basic version
		{"v1.0.0", Semver{1, 0, 0, "", ""}, false},                      // Leading 'v'
		{"1.2", Semver{}, true},                                         // Missing patch
		{"1", Semver{}, true},                                           // Missing minor and patch
		{"1.2.3-alpha", Semver{1, 2, 3, "alpha", ""}, false},            // Prerelease
		{"1.2.3-alpha.1", Semver{1, 2, 3, "alpha.1", ""}, false},        // Complex prerelease
		{"1.2.3+build", Semver{1, 2, 3, "", "build"}, false},            // Build metadata
		{"1.2.3-alpha+build", Semver{1, 2, 3, "alpha", "build"}, false}, // Both
		{"", Semver{}, true},                                            // Empty string
		{"1.2.x", Semver{}, true},                                       // Non-numeric component
		{"1.2.3-alpha.", Semver{}, true},                                // Empty prerelease
		{"1.2.3+", Semver{}, true},                                      // Empty build metadata
	}

	for _, tt := range tests {
		actual, err := New(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("New(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if err == nil && actual != tt.expected {
			t.Errorf("New(%q) = %v, expected %v", tt.input, actual, tt.expected)
		}
	}
}

func TestCmp(t *testing.T) {
	tests := []struct {
		a        string
		b        string
		expected int
	}{
		{"1.0.0", "1.0.0", 0},                     // Equal
		{"1.0.0", "2.0.0", -1},                    // Major difference
		{"1.0.0", "1.1.0", -1},                    // Minor difference
		{"1.0.0", "1.0.1", -1},                    // Patch difference
		{"1.0.1", "1.0.0", 1},                     // Patch difference (swapped)
		{"1.0.0-alpha", "1.0.0", -1},              // Prerelease vs. release
		{"1.0.0-alpha", "1.0.0-beta", -1},         // Alphabetical prerelease
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, // Alphabetical precedence
		{"1.0.0-beta", "1.0.0-beta.2", -1},        // Length precedence
		{"1.0.0-beta.02", "1.0.0-beta.11", -1},    // Numeric prerelease with leading zeros
		{"1.0.0+build.1", "1.0.0+build.2", 0},     // Build metadata ignored
	}

	for _, tt := range tests {
		actual := Cmp(tt.a, tt.b)
		if actual != tt.expected {
			t.Errorf("Cmp(%q, %q) = %d, expected %d", tt.a, tt.b, actual, tt.expected)
		}
	}
}
