// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bufio" // Added for bufio.Reader
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

type RecordDistEntry struct {
	Path   string
	Size   int64
	SHA256 string
}

type RecordDistInfo struct {
	entries []RecordDistEntry
}

type MetadataDistInfo struct {
	// Name of the package
	MetadataVersion        string
	Name                   string
	Version                string
	Dynamic                []string
	Platform               []string
	SupportedPlatform      []string
	Summary                string
	Description            string
	DescriptionContentType string
	Keywords               string
	HomePage               string
	DownloadURL            string
	Author                 string
	AuthorEmail            string
	Maintainer             string
	MaintainerEmail        string
	License                string
	Classifiers            []string
	RequiresDist           []string
	RequiresPython         string
	RequiresExternal       []string
	ProjectURL             []string
	ProvidesExtra          []string
	ProvidesDist           []string
	ObsoletesDist          []string
	OtherFields            map[string][]string // For unknown fields
	GeneralText            []string            // For non-field lines (e.g. multiline description)
}

var UnstablePypiStabilizers = []Stabilizer{
	StableVersionFile2,
	StableVersionFile,
	StableCommentsCollapse
	StableCrlf,
	StablePypiRecord,
}

// RemovePythonComments takes a byte slice containing Python code,
// removes all single-line comments (starting with #) and multi-line comments (docstrings),
// and returns the modified code as a new byte slice, preserving original indentation.
func RemovePythonComments(pythonCode []byte) ([]byte, error) {
	var outputBuffer bytes.Buffer
	reader := bufio.NewReader(bytes.NewReader(pythonCode))

	// State variables for parsing
	inString := false                // True if currently inside any type of string literal
	currentQuote := byte(0)          // Stores the type of quote for the current string (', ", or 0 for none)
	escaped := false                 // True if the previous character was an escape character '\'
	inMultiLineCommentBlock := false // True if we are inside a multi-line comment block (docstring)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("error reading line: %w", err)
		}

		originalLine := line
		trimmedLine := bytes.TrimSuffix(line, []byte{'\n'})
		trimmedLine = bytes.TrimSuffix(trimmedLine, []byte{'\r'})
		lineStr := string(trimmedLine)
		processedLine := ""

		// --- Step 1: Handle multi-line comment blocks (docstrings) ---
		// If we are currently inside a multi-line comment block
		if inMultiLineCommentBlock {
			// Check for the end of the multi-line comment block
			var closingTripleQuote string
			if currentQuote == '\'' {
				closingTripleQuote = `'''`
			} else { // currentQuote == '"'
				closingTripleQuote = `"""`
			}

			if strings.Contains(lineStr, closingTripleQuote) {
				// Find the first occurrence of the closing triple quote
				endIndex := strings.Index(lineStr, closingTripleQuote) + len(closingTripleQuote)

				// The comment block ends on this line.
				// Any content *after* the closing triple quote needs to be processed.
				lineStr = lineStr[endIndex:]
				inMultiLineCommentBlock = false
				inString = false // Reset string state as we've exited the multi-line string
				currentQuote = 0
				escaped = false
				// Continue processing the rest of this line for new comments/strings
			} else {
				// Still inside a multi-line comment block, skip this entire line.
				if err == io.EOF {
					break
				}
				continue // Move to the next line
			}
		}

		// --- Step 2: Process the current line character by character ---
		// This loop handles single-line comments, and the start of new multi-line comments
		// while correctly identifying content within string literals.
		tempLine := ""
		for i := 0; i < len(lineStr); i++ {
			char := lineStr[i]

			// Handle escape sequences (e.g., `\"` or `\'`)
			if escaped {
				tempLine += string(char)
				escaped = false
				continue
			}

			if char == '\\' {
				escaped = true
				tempLine += string(char)
				continue
			}

			// Check for triple quotes (start of multi-line string/docstring)
			// Ensure we have enough characters to form a triple quote
			if i+2 < len(lineStr) {
				tripleQuote := lineStr[i : i+3]
				if tripleQuote == `"""` || tripleQuote == `'''` {
					// If we are already inside a single/double quoted string,
					// these triple quotes are just literal characters within that string.
					if inString {
						tempLine += string(char)
						i += 2 // Skip the next two chars of the triple quote
						continue
					} else {
						// Not inside a single/double quoted string, so this is a potential docstring.
						// Check if it's a self-contained triple-quoted string on this line (e.g., `"""abc"""`)
						// This is a heuristic to identify single-line docstrings.
						if strings.Count(lineStr, tripleQuote) >= 2 && strings.Index(lineStr[i+3:], tripleQuote) != -1 {
							// It's a single-line docstring. Remove it from this point.
							lineStr = lineStr[:i] // Truncate the line
							break                 // Stop processing this line
						} else {
							// It's the start of a multi-line docstring.
							inMultiLineCommentBlock = true
							currentQuote = tripleQuote[0] // Store the type of quote (', or ") for closing
							lineStr = lineStr[:i]         // Truncate the line
							break                         // Stop processing this line
						}
					}
				}
			}

			// Check for single/double quotes (start/end of single-line string)
			// Only toggle string state if not currently in a multi-line comment block
			// and not already in a string of the same type (unless it's the closing quote).
			if char == '\'' && !inMultiLineCommentBlock {
				if inString && currentQuote == '\'' {
					inString = false
					currentQuote = 0
				} else if !inString && currentQuote == 0 { // Only start if not already in another string type
					inString = true
					currentQuote = '\''
				}
			} else if char == '"' && !inMultiLineCommentBlock {
				if inString && currentQuote == '"' {
					inString = false
					currentQuote = 0
				} else if !inString && currentQuote == 0 { // Only start if not already in another string type
					inString = true
					currentQuote = '"'
				}
			}

			// Check for single-line comments (#)
			// A '#' is a comment ONLY if it's not inside any string literal
			// and not currently inside a multi-line comment block.
			if char == '#' && !inString && !inMultiLineCommentBlock {
				lineStr = lineStr[:i] // Truncate the line at the comment
				break                 // Stop processing this line
			}

			tempLine += string(char)
		}
		processedLine = tempLine

		// --- Step 3: Write the processed line to the output buffer ---
		// Only write if the line has content, or if it was an empty line with a newline
		// (to preserve blank lines and overall code structure).
		if processedLine != "" || (len(originalLine) > len(trimmedLine) && strings.TrimSpace(processedLine) == "") {
			outputBuffer.WriteString(processedLine)
			if len(originalLine) > len(trimmedLine) { // If original line had a newline, add it back
				outputBuffer.WriteByte('\n')
			}
		} else if strings.TrimSpace(lineStr) == "" && !inMultiLineCommentBlock {
			// If the line became empty after comment removal (e.g., it was just a comment),
			// and it wasn't part of an ongoing multi-line comment, preserve its newline if it had one.
			if len(originalLine) > len(trimmedLine) {
				outputBuffer.WriteByte('\n')
			}
		}

		// If EOF was reached, break the loop.
		if err == io.EOF {
			break
		}
	}

	return outputBuffer.Bytes(), nil
}

// Helper function to detect structured information
func isStructuredInfo(line []byte) bool {
	// Check for patterns like links, images, or specific formats
	if bytes.HasPrefix(line, []byte(".. image::")) ||
		bytes.HasPrefix(line, []byte(".. _")) ||
		bytes.Contains(line, []byte("http://")) ||
		bytes.Contains(line, []byte("https://")) {
		return true
	}
	return false
}

// Common regex patterns for auto-generated docstrings
var patterns = map[string]*regexp.Regexp{
	"sphinx_param":   regexp.MustCompile(`:param [a-zA-Z_]+:`),
	"sphinx_return":  regexp.MustCompile(`:rtype:`),
	"google_args":    regexp.MustCompile(`(?m)^\s*Args:`),
	"google_returns": regexp.MustCompile(`(?m)^\s*Returns:`),
	"numpy_params":   regexp.MustCompile(`(?m)^\s*Parameters\n[-]+`),
	"numpy_returns":  regexp.MustCompile(`(?m)^\s*Returns\n[-]+`),
}

// Heuristic check for auto-generated docstrings
func mayContainGeneratedDocstring(content string) bool {
	for _, re := range patterns {
		if re.MatchString(content) {
			return true
		}
	}
	return false
}

var StableCommentsCollapse = ZipArchiveStabilizer{
	Name: "comments-collapse",
	Func: func(zr *archive.MutableZipReader) {
		for _, zf := range zr.File {
			if strings.HasSuffix(zf.Name, ".py") {
				println("Processing Python file for comment/docstring checks:", zf.Name)

				r, err := zf.Open()
				if err != nil {
					println("Error opening Python file:", err)
					continue
				}

				originalContent, err := io.ReadAll(r)
				if err != nil {
					println("Error reading Python file content:", err)
					continue
				}

				// Check if the content contains signs of auto-generated docstrings
				if mayContainGeneratedDocstring(string(originalContent)) {
					println("File likely contains auto-generated docstrings, applying stabilizer:", zf.Name)

					cleanedContent, err := RemovePythonComments(originalContent)
					if err != nil {
						println("Error removing comments from Python file:", err)
						continue
					}

					zf.SetContent(cleanedContent)
					println("Comments/docstrings removed from:", zf.Name)
				}
			}
		}
	},
}

func computeSHA256Base64(data []byte) string {
	h := sha256.New()
	h.Write(data)
	sum := h.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum)
}

var StablePypiRecord = ZipArchiveStabilizer{
	Name: "pypi-record",
	Func: func(zr *archive.MutableZipReader) {
		// Only process RECORD files
		var newRecordFile RecordDistInfo
		newRecordFile.entries = make([]RecordDistEntry, 0)

		for _, zf := range zr.File {
			if !strings.HasSuffix(zf.Name, "RECORD") {
				// Recompute the RECORD entry for this file
				content, err := zf.Open()
				if err != nil {
					println("Error opening RECORD file:", err)
					continue
				}
				data, err := io.ReadAll(content)
				if err != nil {
					println("Error reading RECORD file:", err)
					continue
				}
				sha256sum := computeSHA256Base64(data)
				size := len(data)
				// Format: path,sha256=...,size
				newRecordFile.entries = append(newRecordFile.entries, RecordDistEntry{
					Path:   zf.Name,
					Size:   int64(size),
					SHA256: sha256sum,
				})

			}

		}
		// Replace the RECORD file with the new computed entries
		for _, zf := range zr.File {
			if strings.HasSuffix(zf.Name, "RECORD") {
				var buf strings.Builder
				for _, entry := range newRecordFile.entries {
					// Format: path,sha256=...,size
					buf.WriteString(entry.Path)
					buf.WriteString(",sha256=")
					buf.WriteString(entry.SHA256)
					buf.WriteString(",")
					buf.WriteString(fmt.Sprintf("%d", entry.Size))
					buf.WriteString("\n")
				}
				zf.SetContent([]byte(buf.String()))
				break
			}
		}

	},
}

// TODO - Try this by having git config --global core.autocrlf true in the build instead of here
var StableCrlf = ZipEntryStabilizer{
	Name: "crlf",
	Func: func(zf *archive.MutableZipFile) {
		r, err := zf.Open()
		if err != nil {
			println("Error opening file:", err)
			return
		}
		data, err := io.ReadAll(r)
		if err != nil {
			println("Error reading file:", err)
			return
		}
		// Replace all \r\n with \n
		normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		zf.SetContent(normalized)
	},
}

// TODO - Investigate where this is specifically used for
// I only found matching hits to the regex for this in pypa/setuptools and pypa/pipenv no where else
var StableVersionFile = ZipArchiveStabilizer{
	Name: "version-file",
	Func: func(zr *archive.MutableZipReader) {

		// Define the pattern to find
		patternToFind := regexp.MustCompile(`(?m)^TYPE_CHECKING = False\nif TYPE_CHECKING:\n(\s*)from typing import Tuple, Union\n(\s*)VERSION_TUPLE = Tuple\[Union\[int, str\], \.\.\.\]$`)

		// Define the replacement pattern
		replacementPattern := `
__all__ = ["__version__", "__version_tuple__", "version", "version_tuple"]

TYPE_CHECKING = False
if TYPE_CHECKING:
    from typing import Tuple
    from typing import Union

    VERSION_TUPLE = Tuple[Union[int, str], ...]`

		for _, zf := range zr.File {
			// Check if the file is a Python file (e.g., ends with .py)
			if strings.HasSuffix(zf.Name, "version.py") || strings.HasSuffix(zf.Name, "_version.py") {
				println("Processing Python file for type checking conversion:", zf.Name)

				// Open the file content
				r, err := zf.Open()
				if err != nil {
					println("Error opening Python file:", err)
					continue
				}

				// Read the entire content of the file
				originalContent, err := io.ReadAll(r)
				if err != nil {
					println("Error reading Python file content:", err)
					continue
				}

				// Perform the replacement
				modifiedContent := patternToFind.ReplaceAllString(string(originalContent), replacementPattern)

				// Set the modified content back to the zip file
				zf.SetContent([]byte(modifiedContent))
				println("Type checking pattern converted in:", zf.Name)
			}
		}

	},
}

// NOTE - This may be an unsafe change.
// TODO - Investiage need since it could delete important file changes
var StableVersionFile2 = ZipArchiveStabilizer{
	Name: "version-file-2",
	Func: func(zr *archive.MutableZipReader) {

		// originalArchiveHash := getArchiveHash(zr)
		for _, zf := range zr.File {
			// Only process METADATA files
			if !strings.HasSuffix(zf.Name, "_version.py") {
				continue
			}
			println("Processing file:", zf.Name)
			// rebuild the zip without the Description.rst file
			// as it is not needed for stabilization.

			zf.SetContent([]byte("This needed to change (version file)"))

		}
	},
}
