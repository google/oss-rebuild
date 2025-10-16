// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/zip"
	"fmt"
)

// formatMethod maps common zip methods to strings.
func formatMethod(method uint16) string {
	switch method {
	case zip.Store:
		return "Store"
	case zip.Deflate:
		return "Deflate"
	default:
		// Fallback to hex for other/unknown methods
		return fmt.Sprintf("0x%04X", method)
	}
}

// formatZipListing produces a one-line summary of a zip file entry
// -rw-r--r-- Deflate 4          1980-01-01 00:00:00.000000 foo.txt
func formatZipListing(f *zip.FileHeader) string {
	return fmt.Sprintf("%-10s %-8s %-12d %-26s %s\n",
		f.Mode().String(),
		formatMethod(f.Method),
		f.UncompressedSize64,
		f.Modified.UTC().Format("2006-01-01 15:04:05.000000"),
		f.Name,
	)
}
