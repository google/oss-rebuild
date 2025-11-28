// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"

	"github.com/pkg/errors"
)

// NewContentSummary constructs a ContentSummary for the given archive format.
func NewContentSummary(src io.Reader, f Format) (*ContentSummary, error) {
	switch f {
	case ZipFormat:
		srcReader, size, err := ToZipCompatibleReader(src)
		if err != nil {
			return nil, errors.Wrap(err, "converting reader")
		}
		zr, err := zip.NewReader(srcReader, size)
		if err != nil {
			return nil, errors.Wrap(err, "initializing zip reader")
		}
		return NewContentSummaryFromZip(zr)
	case TarGzFormat:
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return nil, errors.Wrap(err, "initializing gzip reader")
		}
		defer gzr.Close()
		return NewContentSummaryFromTar(tar.NewReader(gzr))
	default:
		return nil, errors.New("unsupported archive type")
	}
}
