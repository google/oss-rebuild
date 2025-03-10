// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package glob

import (
	"testing"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern  string
		path     string
		expected bool
		hasError bool
	}{
		// No **
		{"abc", "abc", true, false},
		{"a*c", "abc", true, false},
		{"a?c", "abc", true, false},
		{"a[b]c", "abc", true, false},
		{"a/b/c", "a/b/c", true, false},
		{"a/*/c", "a/b/c", true, false},
		{"a/?/c", "a/b/c", true, false},

		// Simple ** cases
		{"**", "", true, false},
		{"**", "a", true, false},
		{"**", "a/b/c", true, false},
		{"**", "/a", true, false},
		{"**", "a/", true, false},
		// Prefix
		{"a/**", "a", false, false},
		{"a/**", "a/", true, false},
		{"a/**", "a/b", true, false},
		{"a/**", "a/b/c", true, false},
		// Suffix
		{"**/a", "a", false, false},
		{"**/c", "a/b/c", true, false},
		// Two-sided
		{"a/**/c", "b/c", false, false},
		{"a/**/c", "a/b/d", false, false},
		{"a/**/c", "a/c", true, false},
		{"a/**/c", "a/b/c", true, false},
		{"a/**/c", "a/b/b/c", true, false},
		{"a/**/c", "a/b/b/b/c", true, false},
		{"a/**/c", "a/b/d/c", true, false},
		{"a/**/c", "a/bb/c", true, false},
		{"a/**/c", "a/b/c/d", false, false},
		{"a/**/c/*", "a/b/c/d", true, false},
		{"a/**/*", "a/", true, false},
		{"a/**/*", "a/a", true, false},
		{"/**/", "", false, false},
		{"/**/", "/", true, false},
		{"/**/", "/a", false, false},
		{"/**/", "/a/", true, false},
		{"/**", "/", true, false},
		{"/**", "/a", true, false},

		// Invalid patterns
		{"a/**/**", "", false, true},
		{"a**b", "", false, true},
		{"a/**b", "", false, true},
		{"a**", "", false, true},
		{"***", "", false, true},
		{"**/a/**", "", false, true},
		{"a/[a]**", "", false, true},
		{"a/**/c/**/d", "a/b/c/d", false, true},
		{"a/**/c/**/d", "a/b/c/b/d", false, true},
	}
	for _, test := range tests {
		result, err := Match(test.pattern, test.path)

		if (err != nil) != test.hasError {
			t.Errorf("Match(%q, %q) error = %v, hasError = %v", test.pattern, test.path, err, test.hasError)
			continue
		}
		if test.hasError {
			continue
		}
		if result != test.expected {
			t.Errorf("Match(%q, %q) = %v, expected %v",
				test.pattern, test.path, result, test.expected)
		}
	}
}
