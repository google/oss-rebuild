// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

var errFinalWrite = errors.New("final write failed")

type finalWriteError struct{}

func (finalWriteError) Write([]byte) (int, error) {
	return 0, errFinalWrite
}

type gzipTrailerError struct{}

func (gzipTrailerError) Write(p []byte) (int, error) {
	if len(p) == 8 {
		return 0, errFinalWrite
	}
	return len(p), nil
}

func TestStabilizeReportsArchiveFinalizationError(t *testing.T) {
	for _, format := range []archive.Format{
		archive.ZipFormat,
		archive.TarFormat,
	} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			err := Stabilize(finalWriteError{}, bytes.NewReader(emptyArchive(t, format)), format)
			if !errors.Is(err, errFinalWrite) {
				t.Fatalf("Stabilize() error = %v, want %v", err, errFinalWrite)
			}
		})
	}
}

func TestStabilizeReportsGzipTrailerError(t *testing.T) {
	for _, format := range []archive.Format{
		archive.GzipFormat,
		archive.TarGzFormat,
	} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			err := Stabilize(gzipTrailerError{}, bytes.NewReader(emptyArchive(t, format)), format)
			if !errors.Is(err, errFinalWrite) {
				t.Fatalf("Stabilize() error = %v, want %v", err, errFinalWrite)
			}
		})
	}
}

func emptyArchive(t *testing.T, format archive.Format) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case archive.ZipFormat:
		w := zip.NewWriter(&buf)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	case archive.TarFormat:
		w := tar.NewWriter(&buf)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	case archive.GzipFormat:
		w := gzip.NewWriter(&buf)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	case archive.TarGzFormat:
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported format %v", format)
	}
	return buf.Bytes()
}
