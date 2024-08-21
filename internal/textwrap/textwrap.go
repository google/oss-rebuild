package textwrap

import (
	"strings"
)

// Dedent removes common leading whitespace from each line.
//
// Adapted from Python's textwrap.dedent.
//
// Note: Only tabs and spaces are considered whitespace.
// Note: Blank lines are normalized to a newline character.
func Dedent(text string) string {
	isSpaceOrTab := func(r rune) bool { return r == ' ' || r == '\t' }
	lines := strings.Split(text, "\n")

	// Find the text's common indent.
	var commonIndent string
	var foundIndent bool
	for _, line := range lines {
		if len(line) == 0 {
			continue // Skip blank lines
		}
		content := strings.TrimLeftFunc(line, isSpaceOrTab)
		indent := line[:len(line)-len(content)]
		if !foundIndent {
			commonIndent = indent
		} else if strings.HasPrefix(indent, commonIndent) {
			// More indented -> no change
			continue
		} else if strings.HasPrefix(commonIndent, indent) {
			// Less indented -> update to this indent
			commonIndent = indent
		} else {
			// Mismatched indent -> update to largest common prefix
			for i := range min(len(commonIndent), len(indent)) {
				if commonIndent[i] != indent[i] {
					commonIndent = commonIndent[:i]
					break
				}
			}
		}
		foundIndent = true
	}

	// Remove the common indent from each line.
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			result = append(result, strings.TrimLeftFunc(line, isSpaceOrTab))
		} else {
			result = append(result, strings.TrimPrefix(line, commonIndent))
		}
	}

	return strings.Join(result, "\n")
}
