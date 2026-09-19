// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package target names what a rebuild targets: an ecosystem, a package, a
// version and an artifact. It is a leaf so that tools which only need to
// identify an artifact, such as stabilization, do not depend on the rebuild
// machinery.
package target

import (
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

// Ecosystem represents a package ecosystem.
type Ecosystem string

// Ecosystem constants. These are used to select an ecosystem, and used as prefixes in storage.
const (
	NPM      Ecosystem = "npm"
	PyPI     Ecosystem = "pypi"
	CratesIO Ecosystem = "cratesio"
	Maven    Ecosystem = "maven"
	Debian   Ecosystem = "debian"
	RubyGems Ecosystem = "rubygems"
	OCI      Ecosystem = "oci"
)

// Target is a single target we might attempt to rebuild.
type Target struct {
	Ecosystem Ecosystem
	Package   string
	Version   string
	Artifact  string
}

// ArchiveType is the archive format of the Target's artifact.
func (t Target) ArchiveType() archive.Format {
	switch t.Ecosystem {
	case Debian:
		return archive.RawFormat
	case CratesIO, NPM:
		return archive.TarGzFormat
	case PyPI:
		switch {
		case strings.HasSuffix(t.Artifact, ".whl"), strings.HasSuffix(t.Artifact, ".zip"):
			return archive.ZipFormat
		case strings.HasSuffix(t.Artifact, ".tar.gz"):
			return archive.TarGzFormat
		// Deprecated in https://peps.python.org/pep-0715/
		case strings.HasSuffix(t.Artifact, ".egg"):
			return archive.ZipFormat
		// Deprecated in https://peps.python.org/pep-0527/
		case strings.HasSuffix(t.Artifact, ".tgz"), strings.HasSuffix(t.Artifact, ".tar.Z"):
			return archive.TarGzFormat
		case strings.HasSuffix(t.Artifact, ".tar"):
			return archive.TarFormat
		case strings.HasSuffix(t.Artifact, ".tar.bz2"), strings.HasSuffix(t.Artifact, ".tbz"):
			return archive.UnknownFormat // bzip2
		case strings.HasSuffix(t.Artifact, ".tar.xz"):
			return archive.UnknownFormat // xz
		default:
			return archive.UnknownFormat
		}
	case Maven:
		if strings.HasSuffix(t.Artifact, ".jar") {
			return archive.ZipFormat
		} else if strings.HasSuffix(t.Artifact, ".pom") {
			return archive.RawFormat
		}
		return archive.UnknownFormat
	case RubyGems:
		// Gem files are tar archives containing data.tar.gz, metadata.gz, and checksums.yaml.gz
		return archive.TarFormat
	case OCI:
		return archive.TarFormat
	default:
		return archive.UnknownFormat
	}
}
