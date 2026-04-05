// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

var AllPypiStabilizers = []Stabilizer{
	RemoveMetadataJSON,
	StableWheelBuildMetadata,
	StablePypiDescription,
}

func ParseMetadataDistInfo(r io.Reader) (*mail.Message, error) {
	content, err := mail.ReadMessage(r)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(content.Body)
	if err != nil {
		return nil, err
	}

	bodyStr := string(body)
	lines := strings.Split(strings.ReplaceAll(bodyStr, "\r\n", "\n"), "\n")

	if len(lines) > 0 && bodyStr != "" {
		content.Header["MessageBodyDescription"] = lines
	}

	return content, nil
}

// TODO - Add more stabilization rather than a full remove
var RemoveMetadataJSON = ZipArchiveStabilizer{
	Name: "pypi-metadata",
	Func: func(zr *archive.MutableZipReader) {
		for _, zf := range zr.File {
			// Only process metadata.json files
			if !strings.HasSuffix(strings.ToLower(zf.Name), "/metadata.json") {
				continue
			}

			zf.SetContent([]byte("This needed to change (metadata)"))

		}
	},
}

var StablePypiDescription = ZipArchiveStabilizer{
	Name: "pypi-description",
	Func: func(zr *archive.MutableZipReader) {
		for _, zf := range zr.File {
			// Only process DESCRIPTION files
			if !strings.HasSuffix(strings.ToLower(zf.Name), "/description.rst") {
				continue
			}
			// rebuild the zip without the Description.rst file
			// as it is not needed.

			zf.SetContent([]byte("This needed to change (description)"))

		}
	},
}

var StableWheelBuildMetadata = ZipEntryStabilizer{
	Name: "whl-build-metadata",
	Func: func(zf *archive.MutableZipFile) {
		// Only process METADATA files
		if !strings.HasSuffix(zf.Name, "dist-info/METADATA") {
			return
		}

		// Open the existing METADATA file
		r, err := zf.Open()
		if err != nil {
			return // Skip any error
		}

		// Parse the METADATA file
		manifest, err := ParseMetadataDistInfo(r)
		if err != nil {
			return // Skip any error
		}

		// Modify the parsed metadata
		if manifest.Header.Get("Author-Email") == "UNKNOWN" {
			manifest.Header["Author-Email"][0] = ""
		}

		// Serialize the updated metadata back to a string
		var updatedMetadata strings.Builder

		keys := make([]string, 0, len(manifest.Header))
		for key := range manifest.Header {
			if key != "MessageBodyDescription" {
				keys = append(keys, key)
			}
		}

		sort.Strings(keys)

		// TODO - We removed this in the past but added it back for now.
		//   Experiment with removing if stabilzier is inneficient
		keys = append(keys, "MessageBodyDescription")

		for _, key := range keys {
			values := manifest.Header[key]
			if key != "MessageBodyDescription" {
				sort.Strings(values)
			}
			for _, value := range values {
				updatedMetadata.WriteString(fmt.Sprintf("%s: %s\n", key, value))
			}
		}
		updatedMetadata.WriteString("\n") // End of headers

		// Write the updated metadata back into the Zip archive
		zf.SetContent([]byte(updatedMetadata.String()))
	},
}
