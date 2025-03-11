// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package glob provides a path.Match wrapper with support for **

package glob

import (
	"errors"
	"path"
	"strings"
)

// Match extends path.Match to support the '**' glob pattern
// - '**' matches zero or more directory levels
// - '**' must appear at most once in the pattern
// - '**' must be preceded and succeeded by '/' characters or by the beginning/end of the pattern
func Match(pattern, name string) (bool, error) {
	// Fast path: Fall back to path.Match when no special ** pattern
	if !strings.Contains(pattern, "**") {
		return path.Match(pattern, name)
	}
	if err := validateGlobstarPattern(pattern); err != nil {
		return false, err
	}
	// Match the pattern before and after ** separately
	parts := strings.Split(pattern, "**")
	prefixPattern, suffixPattern := parts[0], parts[1]
	if prefixPattern != "" {
		// Get the prefix of name that should match the prefix pattern
		// by taking the number of path components before the **
		namePrefixEnd := getPrefixEnd(name, strings.Count(prefixPattern, "/"))
		if namePrefixEnd == -1 || len(name) < namePrefixEnd {
			return false, nil
		}
		namePrefix := name[:namePrefixEnd]
		prefixMatch, err := path.Match(prefixPattern, namePrefix)
		if err != nil || !prefixMatch {
			return false, err
		}
	}
	if suffixPattern != "" {
		// Get the suffix of name that should match the suffix pattern
		// by taking the number of path components after the **
		nameSuffixStart := getSuffixStart(name, strings.Count(suffixPattern, "/"))
		if nameSuffixStart == -1 || nameSuffixStart > len(name) {
			return false, nil
		}
		nameSuffix := name[nameSuffixStart:]
		suffixMatch, err := path.Match(suffixPattern, nameSuffix)
		if err != nil || !suffixMatch {
			return false, err
		}
	}
	return true, nil
}

func validateGlobstarPattern(pattern string) error {
	// Only one ** may be present
	count := strings.Count(pattern, "**")
	if count > 1 {
		return errors.New("invalid pattern: only one '**' is permitted")
	}
	idx := strings.Index(pattern, "**")
	if idx == -1 {
		return nil
	}
	// Character before ** must be /
	if idx > 0 && pattern[idx-1] != '/' {
		return errors.New("invalid pattern: '**' must be surrounded by slashes or be at start/end of pattern")
	}
	// Character after ** must be /
	if idx+2 < len(pattern) && pattern[idx+2] != '/' {
		return errors.New("invalid pattern: '**' must be surrounded by slashes or be at start/end of pattern")
	}
	return nil
}

// getPrefixEnd returns the end index in name after patternSlashes slashes
// or -1 if not enough slashes exist
func getPrefixEnd(name string, patternSlashes int) int {
	if patternSlashes == 0 {
		return 0
	}
	slashes := 0
	for i, c := range name {
		if c == '/' {
			slashes++
			if slashes == patternSlashes {
				return i + 1 // End after the slash
			}
		}
	}
	return -1 // Not enough slashes
}

// getSuffixStart returns the start index in name before patternSlashes slashes from the end
// or -1 if not enough slashes exist
func getSuffixStart(name string, patternSlashes int) int {
	if patternSlashes == 0 {
		return len(name)
	}
	slashes := 0
	for i := range name {
		c := name[len(name)-i-1]
		if c == '/' {
			slashes++
			if slashes == patternSlashes {
				return len(name) - i - 1 // Start at this slash
			}
		}
	}
	return -1 // Not enough slashes
}
