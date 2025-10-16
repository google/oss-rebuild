// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"fmt"
	"os"
)

// formatTarListing produces a one-line summary of a tar file entry
// -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file.txt
func formatTarListing(h *tar.Header) string {
	return fmt.Sprintf("%-10s %d %d %12d %-26s %s\n",
		os.FileMode(h.Mode).String(),
		h.Uid,
		h.Gid,
		h.Size,
		h.ModTime.UTC().Format("2006-01-01 15:04:05.000000"),
		h.Name,
	)
}
