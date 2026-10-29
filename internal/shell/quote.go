// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package shell quotes values for use in shell scripts.
package shell

import (
	"regexp"
	"strings"
)

// safe matches words the shell passes through verbatim.
var safe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// Quote returns s as one shell word, quoting it only if the shell would
// otherwise split, expand, or interpret it.
func Quote(s string) string {
	if safe.MatchString(s) {
		return s
	}
	return SingleQuote(s)
}

// SingleQuote returns s as one single-quoted shell word.
func SingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var doubleQuoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")

// DoubleQuote returns s as one double-quoted shell word.
func DoubleQuote(s string) string {
	return `"` + doubleQuoteEscaper.Replace(s) + `"`
}
