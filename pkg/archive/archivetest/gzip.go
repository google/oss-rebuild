// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archivetest

import (
	"bytes"
	"compress/gzip"
)

// GzFile creates a gzip-compressed buffer with the given header and content.
// Zero-valued header fields use gzip.Writer defaults (OS=255, no compression level override).
// The compression level is gzip.DefaultCompression unless overridden.
func GzFile(content []byte, header gzip.Header, compression ...int) (*bytes.Buffer, error) {
	level := gzip.DefaultCompression
	if len(compression) > 0 {
		level = compression[0]
	}
	buf := new(bytes.Buffer)
	w, err := gzip.NewWriterLevel(buf, level)
	if err != nil {
		return nil, err
	}
	w.Header = header
	if _, err := w.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}
