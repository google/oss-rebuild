// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package archive provides common types and functions for archive processing.
package archive

// Format represents the archive types of packages.
type Format int

// ArchiveType constants specify the type of archive of a file/target.
const (
	UnknownFormat Format = iota
	TarGzFormat
	TarFormat
	ZipFormat
	RawFormat
)

// Layers returns the number of nested archive layers for a format.
func (f Format) Layers() int {
	switch f {
	case TarGzFormat:
		return 2 // gzip -> tar
	case TarFormat:
		return 1
	case ZipFormat:
		return 1
	default:
		return 0
	}
}
