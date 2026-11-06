// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package shell

import (
	"testing"

	bash "github.com/msuozzo/bonsai/bonsai-bash"
)

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
		{"a'b'c", `'a'\''b'\''c'`},
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

// TestSingleQuoteIsOneLiteralWord parses tricky commands once quoted: each must
// be one argument, with nothing in it for the shell to expand.
func TestSingleQuoteIsOneLiteralWord(t *testing.T) {
	for _, in := range []string{
		`echo hi`,
		`echo 'single quoted'`,
		`python3 -c "import sys; print('ok')"`,
		`a'b'c`,
		`$(touch pwned)`,
		"`whoami`",
		`back\slash and "double"`,
		"tab\tand space",
	} {
		src := []byte("echo " + SingleQuote(in))
		root, err := bash.NewParser().Parse(src)
		if err != nil {
			t.Errorf("parsing %q: %v", src, err)
			continue
		}
		if root.HasError() {
			t.Errorf("%q doesn't parse cleanly", src)
		}
		var args int
		for cmd := range root.Find(bash.KindCommand) {
			for _, c := range cmd.Children {
				if c.Field == bash.FieldArgument {
					args++
				}
			}
		}
		if args != 1 {
			t.Errorf("%q has %d arguments, want 1", src, args)
		}
		for _, kind := range []string{bash.KindCommandSubstitution, bash.KindExpansion, bash.KindSimpleExpansion} {
			for n := range root.Find(kind) {
				t.Errorf("%q expands %q", src, n.Text(src))
			}
		}
	}
}
