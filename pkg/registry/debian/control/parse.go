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
	for field, values := range stanza.Fields {
		v := strings.Join(values, "\n")
		switch field {
		case "Format":
			bi.Format = v
		case "Source":
			bi.Source = v
		case "Binary":
			bi.Binary = values
		case "Architecture":
			bi.Architecture = v
		case "Host-Architecture":
			bi.HostArchitecture = v
		case "Version":
			bi.Version = v
		case "Binary-Only-Changes":
			bi.BinaryOnlyChanges = v
		case "Checksums-Md5":
			bi.ChecksumsMd5 = values
		case "Checksums-Sha1":
			bi.ChecksumsSha1 = values
		case "Checksums-Sha256":
			bi.ChecksumsSha256 = values
		case "Build-Origin":
			bi.BuildOrigin = v
		case "Build-Architecture":
			bi.BuildArchitecture = v
		case "Build-Date":
			bi.BuildDate = v
		case "Build-Kernel-Version":
			bi.BuildKernelVersion = v
		case "Build-Path":
			bi.BuildPath = v
		case "Build-Tainted-By":
			bi.BuildTaintedBy = v
		case "Installed-Build-Depends":
			bi.InstalledBuildDepends = []string{}
			for _, val := range values {
				bi.InstalledBuildDepends = append(bi.InstalledBuildDepends, strings.TrimSuffix(strings.TrimSpace(val), ","))
			}
		case "Environment":
			bi.Environment = values
		default:
			return nil, errors.Errorf("unexpected field: %s", field)
		}
	}
	return &bi, nil
}
