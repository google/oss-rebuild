// Package ini provides a generic INI file parser that supports multiline values,
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
	if s, exists := f.Sections[name]; exists {
		return s
	}
	s := &Section{
		Name:   name,
		Values: make(map[string]string),
	}
	f.Sections[name] = s
	return s
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
		currentSection *Section
		currentKey     string
		currentValue   strings.Builder
		inMultiline    bool
		lineNum        int
		keyIndent      int
	)
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		// Handle multiline continuation (lines indented more than the key line)
		if inMultiline {
			lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			if lineIndent > keyIndent && len(line) > lineIndent {
				if trimmed := strings.TrimSpace(line); trimmed != "" {
					currentValue.WriteString("\n")
					currentValue.WriteString(trimmed)
				}
				continue
			}
		}
		// Finalize previous multiline value
		if inMultiline {
			if currentSection == nil {
				currentSection = file.EnsureSection("")
			}
			currentSection.Values[currentKey] = currentValue.String()
			currentValue.Reset()
			inMultiline = false
		}
		// Capture indentation before trimming
		lineIndent := len(line) - len(strings.TrimLeft(line, " \t"))
		line = strings.TrimSpace(line)
		// Skip empty and comment lines
		if len(line) == 0 {
			continue
		}
		if line[0] == '#' || line[0] == ';' {
			continue
		}
		// Handle section headers
		if line[0] == '[' {
			endIdx := strings.LastIndexByte(line, ']')
			if endIdx == -1 {
				return nil, errors.Errorf("line %d: unclosed section header", lineNum)
			}
			sectionName := line[1:endIdx]
			currentSection = file.EnsureSection(sectionName)
			continue
		}
		// Handle key-value pairs
		separatorIdx := strings.IndexAny(line, "=:")
		if separatorIdx == -1 {
			return nil, errors.Errorf("line %d: no key-value separator found", lineNum)
		}
		key := strings.TrimSpace(line[:separatorIdx])
		if key == "" {
			return nil, errors.Errorf("line %d: empty key name", lineNum)
		}
		rawValue := line[separatorIdx+1:]
		// Remove inline comments
		if commentIdx := findInlineComment(rawValue); commentIdx != -1 {
			rawValue = rawValue[:commentIdx]
		}
		value := strings.TrimSpace(rawValue)
		currentKey = key
		keyIndent = lineIndent
		currentValue.WriteString(value)
		inMultiline = true
	}
	// Finalize last value if we were in multiline mode
	if inMultiline && currentKey != "" {
		if currentSection == nil {
			currentSection = file.EnsureSection("")
		}
		currentSection.Values[currentKey] = currentValue.String()
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "error reading input")
	}
	return file, nil
}

// findInlineComment finds the position of an inline comment in a string.
// An inline comment is # or ; that is preceded by whitespace.
func findInlineComment(s string) int {
	for i := range len(s) {
		if s[i] == '#' || s[i] == ';' {
			if i > 0 && unicode.IsSpace(rune(s[i-1])) {
				return i
			}
		}
	}
	return -1
}
