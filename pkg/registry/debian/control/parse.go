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

type BuildInfo struct {
	Format                string
	Source                string
	Binary                []string
	Architecture          string
	HostArchitecture      string
	Version               string
	BinaryOnlyChanges     string
	ChecksumsMd5          []string
	ChecksumsSha1         []string
	ChecksumsSha256       []string
	BuildOrigin           string
	BuildArchitecture     string
	BuildDate             string
	BuildKernelVersion    string
	BuildPath             string
	BuildTaintedBy        string
	InstalledBuildDepends []string
	Environment           []string
}

func ParseBuildInfo(r io.Reader) (*BuildInfo, error) {
	ctrl, err := Parse(r)
	if err != nil {
		return nil, err
	}
	if len(ctrl.Stanzas) == 2 {
		// skip the "Hash" stanza if present
		if _, ok := ctrl.Stanzas[0].Fields["Hash"]; ok {
			ctrl.Stanzas = ctrl.Stanzas[1:]
		}
	}
	if len(ctrl.Stanzas) != 1 {
		return nil, errors.Errorf("unexpected number of stanzas: %d", len(ctrl.Stanzas))
	}
	bi := BuildInfo{}
	stanza := ctrl.Stanzas[0]
	for field, val := range stanza.Fields {
		// NOTE: For simple fields, we'll use AsFolded() just to be overly permissive with our parsing.
		switch field {
		case "Format":
			bi.Format = val.AsFolded()
		case "Source":
			bi.Source = val.AsFolded()
		case "Binary":
			// Binary is a space-separated, folded list of packages.
			bi.Binary = strings.Fields(val.AsFolded())
		case "Architecture":
			bi.Architecture = val.AsFolded()
		case "Host-Architecture":
			bi.HostArchitecture = val.AsFolded()
		case "Version":
			bi.Version = val.AsFolded()
		case "Binary-Only-Changes":
			// This is a multiline field where indentation matters, and "." should be replaced with an empty line.
			changes := []string{}
			for _, line := range val.AsLines() {
				if line == "." {
					changes = append(changes, "")
				} else {
					changes = append(changes, line)
				}
			}
			bi.BinaryOnlyChanges = strings.Join(changes, "\n")
		case "Checksums-Md5":
			bi.ChecksumsMd5 = val.AsLines()
		case "Checksums-Sha1":
			bi.ChecksumsSha1 = val.AsLines()
		case "Checksums-Sha256":
			bi.ChecksumsSha256 = val.AsLines()
		case "Build-Origin":
			bi.BuildOrigin = val.AsFolded()
		case "Build-Architecture":
			bi.BuildArchitecture = val.AsFolded()
		case "Build-Date":
			bi.BuildDate = val.AsFolded()
		case "Build-Kernel-Version":
			bi.BuildKernelVersion = val.AsFolded()
		case "Build-Path":
			bi.BuildPath = val.AsFolded()
		case "Build-Tainted-By":
			bi.BuildTaintedBy = val.AsFolded()
		case "Installed-Build-Depends":
			bi.InstalledBuildDepends = []string{}
			// Build-Depends is a folded field separated by commas.
			for _, dep := range strings.Split(val.AsFolded(), ",") {
				bi.InstalledBuildDepends = append(bi.InstalledBuildDepends, strings.TrimSpace(dep))
			}
		case "Environment":
			bi.Environment = val.AsLines()
		default:
			return nil, errors.Errorf("unexpected field: %s", field)
		}
	}
	return &bi, nil
}
