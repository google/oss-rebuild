// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

var UnstablePypiStabilizers = []Stabilizer{
	StableVersionFile2,
	StableVersionFile,
	StableCrlf,
	StablePypiRecord,
}

func computeSHA256Base64(data []byte) string {
	h := sha256.New()
	h.Write(data)
	sum := h.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum)
}

var StablePypiRecord = ZipArchiveStabilizer{
	Name: "pypi-record",
	Func: func(zr *archive.MutableZipReader) {
		// Only process RECORD files
		var newRecordFile RecordDistInfo
		newRecordFile.entries = make([]RecordDistEntry, 0)

		for _, zf := range zr.File {
			if !strings.HasSuffix(zf.Name, "RECORD") {
				// Recompute the RECORD entry for this file
				content, err := zf.Open()
				if err != nil {
					println("Error opening RECORD file:", err)
					continue
				}
				data, err := io.ReadAll(content)
				if err != nil {
					println("Error reading RECORD file:", err)
					continue
				}
				sha256sum := computeSHA256Base64(data)
				size := len(data)
				// Format: path,sha256=...,size
				newRecordFile.entries = append(newRecordFile.entries, RecordDistEntry{
					Path:   zf.Name,
					Size:   int64(size),
					SHA256: sha256sum,
				})

			}

		}
		// Replace the RECORD file with the new computed entries
		for _, zf := range zr.File {
			if strings.HasSuffix(zf.Name, "RECORD") {
				var buf strings.Builder
				for _, entry := range newRecordFile.entries {
					// Format: path,sha256=...,size
					buf.WriteString(entry.Path)
					buf.WriteString(",sha256=")
					buf.WriteString(entry.SHA256)
					buf.WriteString(",")
					buf.WriteString(fmt.Sprintf("%d", entry.Size))
					buf.WriteString("\n")
				}
				zf.SetContent([]byte(buf.String()))
				break
			}
		}

	},
}

// TODO - Try this by having git config --global core.autocrlf true in the build instead of here
var StableCrlf = ZipEntryStabilizer{
	Name: "crlf",
	Func: func(zf *archive.MutableZipFile) {
		r, err := zf.Open()
		if err != nil {
			println("Error opening file:", err)
			return
		}
		data, err := io.ReadAll(r)
		if err != nil {
			println("Error reading file:", err)
			return
		}
		// Replace all \r\n with \n
		normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		zf.SetContent(normalized)
	},
}

// TODO - Investigate where this is specifically used for
// I only found matching hits to the regex for this in pypa/setuptools and pypa/pipenv no where else
var StableVersionFile = ZipArchiveStabilizer{
	Name: "version-file",
	Func: func(zr *archive.MutableZipReader) {

		// Define the pattern to find
		patternToFind := regexp.MustCompile(`(?m)^TYPE_CHECKING = False\nif TYPE_CHECKING:\n(\s*)from typing import Tuple, Union\n(\s*)VERSION_TUPLE = Tuple\[Union\[int, str\], \.\.\.\]$`)

		// Define the replacement pattern
		replacementPattern := `
__all__ = ["__version__", "__version_tuple__", "version", "version_tuple"]

TYPE_CHECKING = False
if TYPE_CHECKING:
    from typing import Tuple
    from typing import Union

    VERSION_TUPLE = Tuple[Union[int, str], ...]`

		for _, zf := range zr.File {
			// Check if the file is a Python file (e.g., ends with .py)
			if strings.HasSuffix(zf.Name, "version.py") || strings.HasSuffix(zf.Name, "_version.py") {
				println("Processing Python file for type checking conversion:", zf.Name)

				// Open the file content
				r, err := zf.Open()
				if err != nil {
					println("Error opening Python file:", err)
					continue
				}

				// Read the entire content of the file
				originalContent, err := io.ReadAll(r)
				if err != nil {
					println("Error reading Python file content:", err)
					continue
				}

				// Perform the replacement
				modifiedContent := patternToFind.ReplaceAllString(string(originalContent), replacementPattern)

				// Set the modified content back to the zip file
				zf.SetContent([]byte(modifiedContent))
				println("Type checking pattern converted in:", zf.Name)
			}
		}

	},
}

// NOTE - This may be an unsafe change.
// TODO - Investiage need since it could delete important file changes
var StableVersionFile2 = ZipArchiveStabilizer{
	Name: "version-file-2",
	Func: func(zr *archive.MutableZipReader) {

		// originalArchiveHash := getArchiveHash(zr)
		for _, zf := range zr.File {
			// Only process METADATA files
			if !strings.HasSuffix(zf.Name, "_version.py") {
				continue
			}
			println("Processing file:", zf.Name)
			// rebuild the zip without the Description.rst file
			// as it is not needed for stabilization.

			zf.SetContent([]byte("This needed to change (version file)"))

		}
	},
}
