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

type ControlStanza struct {
	Fields map[string][]string
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
	stanza := ControlStanza{Fields: map[string][]string{}}
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
				stanza = ControlStanza{Fields: map[string][]string{}}
				lastField = ""
			}
		} else if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			// Handle continuation lines.
			if lastField != "" {
				stanza.Fields[lastField] = append(stanza.Fields[lastField], strings.TrimSpace(line))
			} else {
				return nil, errors.Errorf("unexpected continuation line")
			}
		} else {
			// Handle new field.
			field, value, found := strings.Cut(line, ":")
			if !found {
				return nil, errors.Errorf("expected new field: %v", line)
			}
			if _, ok := stanza.Fields[field]; ok {
				return nil, errors.Errorf("duplicate field in stanza: %s", field)
			}
			stanza.Fields[field] = []string{}
			// Skip empty first lines (start of a multiline field).
			if strings.TrimSpace(value) != "" {
				stanza.Fields[field] = []string{strings.TrimSpace(value)}
			}
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
