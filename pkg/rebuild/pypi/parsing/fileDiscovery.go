package parsing

import (
	"path/filepath"
	re "regexp"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pkg/errors"
)

var SupportedFileTypes = map[string]bool{
	"pyproject.toml": true,
}

type FoundFile struct {
	Filename   string
	Filetype   string
	Path       string
	FileObject *object.File
}

type FileVerification struct {
	FoundF              FoundFile
	Type                string
	Name                string
	Path                string
	Main                bool
	NameMatch           bool
	VersionMatch        bool
	PartialNameMatch    bool
	PartialVersionMatch bool
	LevDistance         int
}

// minEditDistance computes the Levenshtein distance between two strings.
// Originally found in the maven gradle infer, used to replace fuzzywuzzy
func MinEditDistance(s1, s2 string) int {
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
func verificationScore(v FileVerification) int {
	if v.VersionMatch {
		return 3
	}
	if v.NameMatch {
		return 2
	}
	if v.Main {
		return 1
	}
	return 0
}

// SortVerifications sorts based on score, and uses Name as a tie-breaker.
func SortVerifications(verifications []FileVerification) []FileVerification {
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
			if a.PartialNameMatch && b.PartialNameMatch {
				// Lower is better
				return a.LevDistance < b.LevDistance
			} else if a.PartialNameMatch {
				// If only a, then good
				return true
			} else if b.PartialNameMatch {
				// If only b, then move it up
				return false
			}
		}

		// If scores are equal, we sort by Name lexicographically
		return a.Name < b.Name
	})

	return verifications
}

func NormalizeName(name string) string {
	// Normalizes a package name according to PEP 503.
	normalized := re.MustCompile(`[-_.]+`).ReplaceAllString(name, "-")
	return strings.ToLower(normalized)
}

// Recursively check for build files
func GoDeep(fileType string, tree *object.Tree, name, version string) ([]FoundFile, error) {

	if !SupportedFileTypes[fileType] {
		return nil, errors.New("unsupported file type")
	}

	var foundFiles []FoundFile

	result := tree.Files()
	result.ForEach(func(f *object.File) error {
		if filepath.Base(f.Name) == fileType {
			foundFiles = append(foundFiles, FoundFile{
				Filename:   f.Name,
				Filetype:   fileType,
				Path:       filepath.Dir(f.Name),
				FileObject: f,
			})
		}
		return nil
	})
	if len(foundFiles) == 0 {
		return nil, errors.New("no matching files found")
	}
	return foundFiles, nil
}
