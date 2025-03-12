// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"slices"

	"github.com/pkg/errors"
)

var AllStabilizers = slices.Concat(AllZipStabilizers, AllTarStabilizers, AllGzipStabilizers, AllJarStabilizers)

// Stabilize selects and applies the default stabilization routine for the given archive format.
func Stabilize(dst io.Writer, src io.Reader, f Format) error {
	return StabilizeWithOpts(dst, src, f, StabilizeOpts{Stabilizers: AllStabilizers})
}

// StabilizeWithOpts selects and applies the provided stabilization routine for the given archive format.
func StabilizeWithOpts(dst io.Writer, src io.Reader, f Format, opts StabilizeOpts) error {
	switch f {
	case ZipFormat:
		srcReader, size, err := toZipCompatibleReader(src)
		if err != nil {
			return errors.Wrap(err, "converting reader")
		}
		zr, err := zip.NewReader(srcReader, size)
		if err != nil {
			return errors.Wrap(err, "initializing zip reader")
		}
		zw := zip.NewWriter(dst)
		defer zw.Close()
		err = StabilizeZip(zr, zw, opts)
		if err != nil {
			return errors.Wrap(err, "stabilizing zip")
		}
	case TarGzFormat:
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return errors.Wrap(err, "initializing gzip reader")
		}
		defer gzr.Close()
		gzw, err := NewStabilizedGzipWriter(gzr, dst, opts)
		if err != nil {
			return errors.Wrap(err, "initializing gzip writer")
		}
		defer gzw.Close()
		err = StabilizeTar(tar.NewReader(gzr), tar.NewWriter(gzw), opts)
		if err != nil {
			return errors.Wrap(err, "stabilizing tar.gz")
		}
	case TarFormat:
		err := StabilizeTar(tar.NewReader(src), tar.NewWriter(dst), opts)
		if err != nil {
			return errors.Wrap(err, "stabilizing tar")
		}
	case RawFormat:
		if _, err := io.Copy(dst, src); err != nil {
			return errors.Wrap(err, "copying raw")
		}
	default:
		return errors.New("unsupported archive type")
	}
	return nil
}

// NewContentSummary constructs a ContentSummary for the given archive format.
func NewContentSummary(src io.Reader, f Format) (*ContentSummary, error) {
	switch f {
	case ZipFormat:
		srcReader, size, err := toZipCompatibleReader(src)
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
