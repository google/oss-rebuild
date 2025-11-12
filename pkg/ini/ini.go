// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package ini provides an INI file parser that supports multiline values,
// comments, and the extended syntax used by Python's setup.cfg files.
package ini

import (
	"bufio"
	"io"
	"strings"
	"unicode"

	"github.com/pkg/errors"
)

// Section represents a section in an INI file with its name and key-value pairs.
type Section struct {
	Name   string
	Values map[string]string
}

// File represents a parsed INI file with its sections.
type File struct {
	Sections map[string]*Section
}

// NewFile creates a new empty INI file.
func NewFile() *File {
	return &File{
		Sections: make(map[string]*Section),
	}
}

// EnsureSection returns the section with the given name, creating it if it doesn't exist.
func (f *File) EnsureSection(name string) *Section {
	if _, exists := f.Sections[name]; !exists {
		f.Sections[name] = &Section{
			Name:   name,
			Values: make(map[string]string),
		}
	}
	return f.Sections[name]
}

// GetValue returns the value for a key in a section, and a boolean indicating if it was found.
func (f *File) GetValue(section, key string) (string, bool) {
	if s, exists := f.Sections[section]; exists {
		if val, exists := s.Values[key]; exists {
			return val, true
		}
	}
	return "", false
}

// Parse parses an INI file from an io.Reader and returns a File structure.
// It supports:
// - Section headers: [section_name]
// - Key-value pairs with = or : separators
// - Comments starting with # or ;
// - Inline comments (# or ; preceded by whitespace)
// - Multiline values (continuation lines indented with spaces or tabs)
func Parse(r io.Reader) (*File, error) {
	scanner := bufio.NewScanner(r)
	file := NewFile()
	var (
		currentSection  *Section
		currentKey      string
		currentValue    strings.Builder
		inMultiline     bool
		lineNum         int
		keyIndent       int
		blankLineBuffer int
	)
	// Callback to save the current accumulated value and reset state
	flush := func() {
		if currentKey == "" {
			return
		}
		if currentSection == nil {
			currentSection = file.EnsureSection("")
		}
		currentSection.Values[currentKey] = currentValue.String()
		currentValue.Reset()
		currentKey = ""
	}
	for scanner.Scan() {
		lineNum++
		rawLine := scanner.Text()
		// 1. Analyze indentation and basic content
		trimmed := strings.TrimLeftFunc(rawLine, unicode.IsSpace)
		indent := len(rawLine) - len(trimmed)
		isComment := len(trimmed) > 0 && (trimmed[0] == '#' || trimmed[0] == ';')
		isEmpty := len(trimmed) == 0
		// 2. Handle Multiline Continuation
		if inMultiline {
			if isComment {
				// Comments ignored and don't break the block
				continue
			} else if isEmpty {
				// Blanks only kept if followed by another continuation
				blankLineBuffer++
				continue
			} else if indent > keyIndent {
				// Line is indented deeper than the key so we're in a continuation
				for range blankLineBuffer {
					currentValue.WriteByte('\n')
				}
				blankLineBuffer = 0
				// Strip inline comments from the value
				if idx := findInlineComment(trimmed); idx != -1 {
					trimmed = trimmed[:idx]
				}
				currentValue.WriteByte('\n')
				currentValue.WriteString(strings.TrimSpace(trimmed))
				continue
			} else {
				// End multiline block
				flush()
				inMultiline = false
				blankLineBuffer = 0
			}
		}
		// 3. Skip empty lines and full-line comments
		if isEmpty || isComment {
			continue
		}
		// 4. Strip inline comments
		line := trimmed
		if idx := findInlineComment(line); idx != -1 {
			line = strings.TrimSpace(line[:idx])
			if len(line) == 0 {
				continue
			}
		}
		// 5. Handle Section Headers
		// Logic: Try to parse as section. If valid, continue.
		// If invalid but contains separator (= or :), fall through to Key-Value handling (Python fallback).
		if line[0] == '[' {
			endIdx := strings.LastIndexByte(line, ']')
			hasSeparator := strings.ContainsAny(line, "=:")
			isValidSection := endIdx != -1 && endIdx > 1 // >1 ensures name isn't empty "[]"
			if isValidSection {
				currentSection = file.EnsureSection(line[1:endIdx])
				continue
			}
			// If not valid, and no separator, it's a hard error
			if !hasSeparator {
				if endIdx == -1 {
					return nil, errors.Errorf("line %d: unclosed section header", lineNum)
				}
				return nil, errors.Errorf("line %d: empty section name", lineNum)
			} else {
				// If hasSeparator, we do nothing here and fall through to kv parsing.
				// NOTE: This is an odd behavior of Python's configparser but easy to support.
			}
		}
		// 6. Handle Key-Value Pairs
		sepIdx := strings.IndexAny(line, "=:")
		if sepIdx == -1 {
			return nil, errors.Errorf("line %d: no key-value separator found", lineNum)
		}
		key := strings.TrimSpace(line[:sepIdx])
		if key == "" {
			return nil, errors.Errorf("line %d: empty key name", lineNum)
		}
		// 7. Flush and init state for multiline continuation
		flush()
		currentKey = key
		keyIndent = indent
		inMultiline = true
		currentValue.WriteString(strings.TrimSpace(line[sepIdx+1:]))
	}
	flush() // last section
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "error reading input")
	}
	return file, nil
}

// findInlineComment finds the position of an inline comment in a string.
// An inline comment is # or ; that is preceded by whitespace.
// Returns the byte index of the comment start, or -1 if not found.
func findInlineComment(s string) int {
	prevRune := rune(-1)
	byteIdx := 0
	for _, r := range s {
		if r == '#' || r == ';' {
			if prevRune != -1 && unicode.IsSpace(prevRune) {
				return byteIdx
			}
		}
		prevRune = r
		byteIdx += len(string(r))
	}
	return -1
}
