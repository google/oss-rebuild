// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package shell

import "testing"

func TestQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"dist/foo-1.0.tar.gz", "dist/foo-1.0.tar.gz"},
		{"", "''"},
		{"foo bar.whl", "'foo bar.whl'"},
		{"$(id)", "'$(id)'"},
		{"*.whl", "'*.whl'"},
		{"~/foo", "'~/foo'"},
		{"it's", `'it'\''s'`},
	} {
		if got := Quote(tc.in); got != tc.want {
			t.Errorf("Quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestSingleQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"curl", "'curl'"},
		{"it's", `'it'\''s'`},
	} {
		if got := SingleQuote(tc.in); got != tc.want {
			t.Errorf("SingleQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestDoubleQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a b", `"a b"`},
		{"a\"$b`c\\", `"a\"\$b\` + "`" + `c\\"`},
	} {
		if got := DoubleQuote(tc.in); got != tc.want {
			t.Errorf("DoubleQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
