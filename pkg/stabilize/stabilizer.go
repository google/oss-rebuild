// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package stabilize provides stabilizers for normalizing archive contents.
package stabilize

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

// Stabilizer is an interface for archive stabilization operations.
type Stabilizer interface {
	Stabilize(any)
}

// StabilizeOpts aggregates stabilizers to be used in stabilization.
type StabilizeOpts struct {
	Stabilizers []Stabilizer
}

func Stabilize(dst io.Writer, src io.Reader, f archive.Format) error {
	return StabilizeWithOpts(dst, src, f, StabilizeOpts{Stabilizers: AllStabilizers})
}

// StabilizeWithOpts selects and applies the provided stabilization routine for the given archive format.
func StabilizeWithOpts(dst io.Writer, src io.Reader, f archive.Format, opts StabilizeOpts) error {
	switch f {
	case archive.ZipFormat:
		srcReader, size, err := archive.ToZipCompatibleReader(src)
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
	case archive.TarGzFormat:
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
	case archive.GzipFormat:
		gzr, err := gzip.NewReader(src)
		if err != nil {
			return errors.Wrap(err, "initializing gzip reader")
		}
		defer gzr.Close()
		gzw, err := NewStabilizedGzipWriter(gzr, dst, opts)
		if err != nil {
			return errors.Wrap(err, "stabilizing gzip")
		}
		defer gzw.Close()
		if _, err := io.Copy(gzw, gzr); err != nil {
			return errors.Wrap(err, "copying gzip content")
		}
	case archive.TarFormat:
		err := StabilizeTar(tar.NewReader(src), tar.NewWriter(dst), opts)
		if err != nil {
			return errors.Wrap(err, "stabilizing tar")
		}
	case archive.RawFormat:
		if _, err := io.Copy(dst, src); err != nil {
			return errors.Wrap(err, "copying raw")
		}
	default:
		return errors.New("unsupported archive type")
	}
	return nil
}
