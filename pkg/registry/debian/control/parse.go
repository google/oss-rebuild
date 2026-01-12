// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package control provides parsing functions for debian control files.
// For more details, see https://www.debian.org/doc/debian-policy/ch-controlfields.html
package control

import (
	"bufio"
	"io"
	"strings"

	"github.com/pkg/errors"
)

type Value struct {
	Lines []string
}

// Simple, Folded, and Multiline are different types of fields in a control file.
// The exact type of a field is defined by the particular control file schema (dsc, buildinfo, etc).
// https://www.debian.org/doc/debian-policy/ch-controlfields.html#syntax-of-control-files
func (v Value) AsSimple() (string, error) {
	if len(v.Lines) != 1 {
		return "", errors.New("expected simple field")
	}
	return v.Lines[0], nil
}

func (v Value) AsFolded() string {
	var out []string
	for _, line := range v.Lines {
		out = append(out, strings.TrimSpace(line))
	}
	return strings.Join(out, " ")
}

func (v Value) AsMultiline() string {
	return strings.Join(v.Lines, "\n")
}

// AsLines returns the lines of the value, stripping the first line if it is empty.
// This is useful for multiline fields (like Checksums) where the data often
// starts on the line following the field name.
func (v Value) AsLines() []string {
	lines := v.Lines
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		return lines[1:]
	}
	return lines
}

// AsList returns the comma-separated values from a folded field, with whitespace trimmed.
func (v Value) AsList() []string {
	l := []string{}
	for _, line := range strings.Split(v.AsFolded(), ",") {
		l = append(l, strings.TrimSpace(line))
	}
	return l
}

type ControlStanza struct {
	Fields map[string]Value
}

type ControlFile struct {
	Stanzas []ControlStanza
}

func Parse(r io.Reader) (*ControlFile, error) {
	b := bufio.NewScanner(r)
	if !b.Scan() {
		return nil, errors.New("failed to scan .dsc file")
	}
	// Skip PGP signature header.
	if strings.HasPrefix(b.Text(), "-----BEGIN PGP SIGNED MESSAGE-----") {
		b.Scan()
	}
	d := ControlFile{}
	stanza := ControlStanza{Fields: map[string]Value{}}
	var lastField string
	for {
		// Check for PGP signature footer.
		if strings.HasPrefix(b.Text(), "-----BEGIN PGP SIGNATURE-----") {
			break
		}
		line := b.Text()
		if strings.TrimSpace(line) == "" {
			// Handle empty lines as stanza separators.
			if len(stanza.Fields) > 0 {
				d.Stanzas = append(d.Stanzas, stanza)
				stanza = ControlStanza{Fields: map[string]Value{}}
				lastField = ""
			}
		} else if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			// Handle continuation lines.
			if lastField != "" {
				// We strip the first character (the continuation marker) but preserve the rest
				// of the line, including any indentation. This allows "multiline" fields to
				// retain their structure. "Folded" values can be trimmed later.
				v := stanza.Fields[lastField]
				v.Lines = append(v.Lines, line[1:])
				stanza.Fields[lastField] = v
			} else {
				return nil, errors.Errorf("unexpected continuation line")
			}
		} else {
			// Handle new field.
			field, value, found := strings.Cut(line, ":")
			if !found {
				return nil, errors.Errorf("expected new field, got: '%v'", line)
			}
			if _, ok := stanza.Fields[field]; ok {
				return nil, errors.Errorf("duplicate field in stanza: %s", field)
			}
			stanza.Fields[field] = Value{Lines: []string{strings.TrimSpace(value)}}
			lastField = field
		}
		if !b.Scan() {
			break
		}
	}
	// Add the final stanza if it's not empty.
	if len(stanza.Fields) > 0 {
		d.Stanzas = append(d.Stanzas, stanza)
	}

	return &d, nil
}
