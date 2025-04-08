// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/pkg/errors"
)

// Implements MANIFEST.MF spec: https://docs.oracle.com/javase/8/docs/technotes/guides/jar/jar.html#JARManifest

// Section represents a section in the manifest file
type Section struct {
	// attributes maintains the mapping of names to values for quick lookup
	attributes map[string]string
	// Names of Attributes, by default maintaining their original order of appearance
	Names []string
}

// NewSection creates a new section
func NewSection() *Section {
	return &Section{
		attributes: make(map[string]string),
		Names:      make([]string, 0),
	}
}

// Set adds or updates an attribute while maintaining order
func (s *Section) Set(name, value string) {
	if _, exists := s.attributes[name]; !exists {
		s.Names = append(s.Names, name)
	}
	s.attributes[name] = value
}

// Get retrieves an attribute value
func (s *Section) Get(name string) (string, bool) {
	v, ok := s.attributes[name]
	return v, ok
}

// Delete removes an attribute
func (s *Section) Delete(name string) {
	if _, ok := s.attributes[name]; !ok {
		return
	}
	delete(s.attributes, name)
	for i, n := range s.Names {
		if n == name {
			s.Names = slices.Delete(s.Names, i, i+1)
			break
		}
	}
}

// Manifest represents a parsed MANIFEST.MF file
type Manifest struct {
	MainSection     *Section
	EntrySections   []*Section
	OriginalContent []byte // Keep original content for modification
}

// NewManifest creates a new empty manifest
func NewManifest() *Manifest {
	return &Manifest{
		MainSection:   NewSection(),
		EntrySections: make([]*Section, 0),
	}
}

// ParseManifest parses a manifest file from a reader
func ParseManifest(r io.Reader) (*Manifest, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	content = normalizeLineEndings(content)

	manifest := NewManifest()
	manifest.OriginalContent = content
	reader := bufio.NewReader(bytes.NewReader(content))

	currentSection := manifest.MainSection
	var currentLine, continuationLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, errors.Wrap(err, "reading line")
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, " ") {
			// Continuation line
			if currentLine == "" {
				return nil, errors.New("unexpected continuation line")
			}
			continuationLine += strings.TrimPrefix(line, " ")
			continue
		}
		currentLine += continuationLine
		continuationLine = ""
		if err := processManifestLine(currentSection, currentLine); err != nil {
			return nil, err
		}
		currentLine = line
		if line == "" {
			// Section separator
			if currentSection != manifest.MainSection && len(currentSection.Names) > 0 {
				manifest.EntrySections = append(manifest.EntrySections, currentSection)
			}
			currentSection = NewSection()
			if err == io.EOF {
				break
			}
		} else if err == io.EOF {
			return nil, errors.New("missing trailing newline")
		}
	}
	return manifest, nil
}

// processManifestLine processes a single manifest line and adds it to the section
func processManifestLine(section *Section, line string) error {
	if line == "" {
		return nil
	}
	colonIdx := strings.Index(line, ":")
	if colonIdx == -1 {
		return fmt.Errorf("invalid manifest line (missing colon): %s", line)
	}
	name := strings.TrimSpace(line[:colonIdx])
	value := strings.TrimPrefix(line[colonIdx+1:], " ")
	if err := validateName(name); err != nil {
		return fmt.Errorf("invalid name '%s': %w", name, err)
	}
	if _, exists := section.Get(name); exists {
		return fmt.Errorf("duplicate attribute: %s", name)
	}
	section.Set(name, value)
	return nil
}

// validateName checks if a manifest attribute name is valid
func validateName(name string) error {
	if len(name) == 0 {
		return errors.New("empty name")
	}
	for _, c := range name {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return errors.Errorf("invalid character in name: %c", c)
		}
	}
	// Check for "From" prefix since someone might think this is an email???
	if strings.HasPrefix(strings.ToLower(name), "from") {
		return errors.New("name cannot start with 'From'")
	}

	return nil
}

// WriteManifest writes a manifest back to a writer
func WriteManifest(w io.Writer, m *Manifest) error {
	if err := writeSection(w, m.MainSection); err != nil {
		return err
	}
	for _, section := range m.EntrySections {
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return err
		}
		if err := writeSection(w, section); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("\r\n"))
	return err
}

// writeSection writes a single section to a writer
func writeSection(w io.Writer, section *Section) error {
	for _, name := range section.Names {
		value, _ := section.Get(name)
		if err := writeAttribute(w, name, value); err != nil {
			return err
		}
	}
	return nil
}

// writeAttribute splits a line longer than 72 bytes into continuation lines
func writeAttribute(w io.Writer, name, value string) error {
	sep := ": "
	remaining := name + sep + value
	base := len(name) + len(sep)
	for len(remaining) > 72 {
		// Find last space before 72 bytes
		splitIdx := 71
		for splitIdx > base && remaining[splitIdx] != ' ' {
			splitIdx--
		}
		if splitIdx == base {
			// No space found, force split at 71
			splitIdx = 71
		}
		if _, err := w.Write([]byte(remaining[:splitIdx+1] + "\r\n")); err != nil {
			return err
		}
		remaining = " " + remaining[splitIdx+1:]
		base = 0
	}
	if _, err := w.Write([]byte(remaining + "\r\n")); err != nil {
		return err
	}
	return nil
}

// normalizeLineEndings ensures consistent CRLF line endings
func normalizeLineEndings(data []byte) []byte {
	// Replace Windows style (CRLF) with Unix style (LF)
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	// Replace Mac style (CR) with Unix style (LF)
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	// Replace Unix style (LF) with Windows style (CRLF)
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	return data
}
