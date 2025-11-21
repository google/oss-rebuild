// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import "compress/gzip"

// MutableGzipHeader wraps gzip.Header to allow modification of compression level.
type MutableGzipHeader struct {
	*gzip.Header
	Compression int
}
