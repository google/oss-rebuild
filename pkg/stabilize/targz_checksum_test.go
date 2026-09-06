// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TestTarGzStabilizationRejectsInvalidGzipChecksum(t *testing.T) {
	var input bytes.Buffer
	gz := gzip.NewWriter(&input)
	tw := tar.NewWriter(gz)
	body := []byte("content")
	if err := tw.WriteHeader(&tar.Header{Name: "file", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), input.Bytes()...)
	corrupt[len(corrupt)-8] ^= 0xff
	var output bytes.Buffer
	if err := StabilizeWithOpts(&output, bytes.NewReader(corrupt), archive.TarGzFormat, StabilizeOpts{}); err == nil {
		t.Fatal("StabilizeWithOpts error = nil for invalid gzip checksum")
	}
}
