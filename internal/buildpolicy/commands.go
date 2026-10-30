// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package buildpolicy

import "strings"

// Commands is a script's command lines, comments and blank lines dropped and
// whitespace collapsed, so two renderings of the same commands compare equal.
func Commands(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		if l := strings.Join(strings.Fields(stripComment(line)), " "); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// stripComment drops a trailing shell comment, ignoring # inside quotes.
func stripComment(line string) string {
	var q rune
	for i, r := range line {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '\'' || r == '"':
			q = r
		case r == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}
	return line
}
