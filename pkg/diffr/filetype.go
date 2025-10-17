// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"errors"
	"io"
)

// FileType defines the identified file type
type FileType int

const (
	// TypeBinary represents an unidentified binary file
	TypeBinary FileType = iota
	// TypeText represents an unidentified text file
	TypeText
	// TypeGzip represents a Gzip compressed file
	TypeGzip
	// TypeZip represents a Zip archive
	TypeZip
	// TypeTar represents a Tar archive (specifically ustar)
	TypeTar
)

func (ft FileType) String() string {
	switch ft {
	case TypeGzip:
		return "gzip"
	case TypeZip:
		return "zip"
	case TypeTar:
		return "tar"
	case TypeText:
		return "text"
	case TypeBinary:
		return "binary"
	default:
		return "unknown"
	}
}

// Magic byte sequences for file type detection
var (
	gzipMagic = []byte{0x1f, 0x8b}
	zipMagic  = []byte{0x50, 0x4B, 0x03, 0x04} // "PK\x03\x04"
	tarMagic  = []byte("ustar")                // At header offset 257
)

const (
	tarMagicOffset = 257
	minPeekSize    = 1024 // Binary detection heuristic requires 1K
)

func isBinary(buf []byte) bool {
	n := len(buf)
	if n == 0 {
		return false // Empty file is considered text
	}
	nonPrintable := 0
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true // Null byte presence indicates a binary file
		}
		if buf[i] <= 31 {
			nonPrintable++
		}
	}
	// Consider it binary If more than a quarter of characters are non-printable.
	return (float64(nonPrintable) / float64(n)) > 0.25
}

// DetectFileType checks the magic bytes of a reader to identify its type.
func DetectFileType(r io.ReadSeeker) (FileType, error) {
	r.Seek(0, io.SeekStart)
	defer r.Seek(0, io.SeekStart)
	var buf [minPeekSize]byte
	n, err := r.Read(buf[:])
	if err != nil && !errors.Is(err, io.EOF) { // EOF means we have the full file
		return TypeBinary, err
	}
	peek := buf[:n]
	switch {
	case bytes.HasPrefix(peek, gzipMagic):
		return TypeGzip, nil
	case bytes.HasPrefix(peek, zipMagic):
		return TypeZip, nil
	case len(peek) >= tarMagicOffset+len(tarMagic) && bytes.Equal(peek[tarMagicOffset:tarMagicOffset+len(tarMagic)], tarMagic):
		return TypeTar, nil
	case isBinary(peek[:min(len(peek), 1024)]):
		return TypeBinary, nil
	default:
		return TypeText, nil
	}
}
