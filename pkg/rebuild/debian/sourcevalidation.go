// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"archive/tar"
	"io"
)

// ExtractTarCommitID reads the git commit ID from a tar archive's PAX headers.
// When git-archive creates a tarball from a commit (not a tree), it embeds
// the commit SHA in a pax global extended header. Go's tar library automatically
// hoists global PAX headers into each record's PAXRecords field as the "comment" key.
//
// This function reads the first tar entry and extracts the commit ID from its
// PAX records.
//
// Reference: https://git-scm.com/docs/git-get-tar-commit-id
func ExtractTarCommitID(r io.Reader) (string, error) {
	tr := tar.NewReader(r)
	// Read the first header (git-archive puts the commit ID in the first entry's PAX records)
	hdr, err := tr.Next()
	if err != nil {
		if err == io.EOF {
			return "", nil // Empty tar
		}
		return "", nil // Invalid or truncated tar - return empty, not an error
	}
	// Check PAX records for the "comment" field containing the commit ID
	if commitID, ok := hdr.PAXRecords["comment"]; ok {
		// Validate it's a proper SHA-1 hash
		if isValidSHA1(commitID) {
			return commitID, nil
		}
	}
	return "", nil
}

// isValidSHA1 checks if a string is a valid SHA-1 hash (40 lowercase hex chars).
func isValidSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
