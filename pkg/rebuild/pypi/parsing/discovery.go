// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"path/filepath"
	re "regexp"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

var supportedFileTypes = map[string]bool{
	"pyproject.toml": true,
	"setup.py":       true,
	"setup.cfg":      true,
}

type foundFile struct {
	name     string
	filetype string
	path     string
	object   *object.File
}

type fileVerification struct {
	foundF              foundFile
	main                bool
	nameMatch           bool
	versionMatch        bool
	partialNameMatch    bool
	partialVersionMatch bool
	levDistance         int
}

// minEditDistance computes the Levenshtein distance between two strings.
// Originally found in the maven gradle infer, used to replace fuzzywuzzy
func minEditDistance(s1, s2 string) int {
	len1 := len(s1)
	len2 := len(s2)
	dp := make([][]int, len1+1)
	for i := range dp {
		dp[i] = make([]int, len2+1)
	}

	for i := 0; i < len1+1; i++ {
		dp[i][0] = i
	}
	for j := 0; j < len2+1; j++ {
		dp[0][j] = j
	}

	for i := 1; i < len1+1; i++ {
		for j := 1; j < len2+1; j++ {
			if s1[i-1] == s2[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
			}
		}
	}

	return dp[len1][len2]
}

// verificationScore assigns a numeric priority to the verification object.
// Higher score means higher priority (comes first).
func verificationScore(v fileVerification) int {
	if v.versionMatch {
		return 3
	}
	if v.nameMatch {
		return 2
	}
	if v.main {
		return 1
	}
	return 0
}

// SortVerifications sorts based on score, and uses Name as a tie-breaker.
func sortVerifications(verifications []fileVerification) []fileVerification {
	sort.Slice(verifications, func(i, j int) bool {
		a := verifications[i]
		b := verifications[j]

		scoreA := verificationScore(a)
		scoreB := verificationScore(b)

		if scoreA != scoreB {
			// If scores are different, the higher score comes first
			return scoreA > scoreB
		} else if scoreA == 0 {
			// Try and compare the Levenshtein distances
			if a.partialNameMatch && b.partialNameMatch {
				// Lower is better
				return a.levDistance < b.levDistance
			} else if a.partialNameMatch {
				// If only a, then good
				return true
			} else if b.partialNameMatch {
				// If only b, then move it up
				return false
			}
		}

		// If scores are equal, we sort by Name lexicographically
		return a.foundF.name < b.foundF.name
	})

	return verifications
}

func normalizeName(name string) string {
	// Normalizes a package name according to PEP 503.
	normalized := re.MustCompile(`[-_.]+`).ReplaceAllString(name, "-")
	return strings.ToLower(normalized)
}

// Recursively check for build files
func findRecursively(fileType string, tree *object.Tree, hintDir string) ([]foundFile, error) {

	if !supportedFileTypes[fileType] {
		return nil, errors.New("unsupported file type")
	}

	var foundFiles []foundFile

	tree.Files().ForEach(func(f *object.File) error {
		if filepath.Base(f.Name) == fileType && (hintDir == "" || filepath.Dir(f.Name) == hintDir) {
			foundFiles = append(foundFiles, foundFile{
				name:     f.Name,
				filetype: fileType,
				path:     filepath.Dir(f.Name),
				object:   f,
			})
		}
		return nil
	})
	if len(foundFiles) == 0 {
		return nil, errors.New("no matching files found")
	}
	return foundFiles, nil
}
