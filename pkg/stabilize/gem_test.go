// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

var stableGzipHeader = gzip.Header{OS: 255}

var stableTarHeader = tar.Header{
	ModTime: epoch, AccessTime: epoch, ChangeTime: time.Time{},
	Mode: 0777, Format: tar.FormatPAX, PAXRecords: map[string]string{"atime": "0"},
}

func TestStableGemExcludeChecksums(t *testing.T) {
	input := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: []byte("data")},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: []byte("meta")},
		{Header: &tar.Header{Name: "checksums.yaml.gz", Typeflag: tar.TypeReg}, Body: []byte("sums")},
	}))
	want := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: []byte("data")},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: []byte("meta")},
	}))
	var got bytes.Buffer
	orDie(StabilizeTar(
		tar.NewReader(bytes.NewReader(input.Bytes())),
		tar.NewWriter(&got),
		NewContext(archive.TarFormat).WithStabilizers([]Stabilizer{StableGemExcludeChecksums}),
	))
	if diff := cmp.Diff(want.Bytes(), got.Bytes()); diff != "" {
		t.Errorf("StabilizeTar() mismatch (-want +got):\n%s", diff)
	}
}

func TestStableGemInnerArchives(t *testing.T) {
	nonEpoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	nonCanonicalGzipHeader := gzip.Header{Name: "metadata", ModTime: nonEpoch}
	metaContent := []byte("name: test-gem\n")

	// --- Input gem: non-canonical inner archives ---
	input := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.TgzFile([]archive.TarEntry{
			{Header: &tar.Header{Name: "lib/foo.rb", Typeflag: tar.TypeReg, ModTime: nonEpoch, Mode: 0644, Uid: 1000, Uname: "dev"}, Body: []byte("puts 'foo'\n")},
		})).Bytes()},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.GzFile(metaContent, nonCanonicalGzipHeader)).Bytes()},
		{Header: &tar.Header{Name: "checksums.yaml.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.GzFile(nil, nonCanonicalGzipHeader)).Bytes()},
	}))

	// --- Expected gem: stabilized inner archives ---
	stableOuterEntry := func(name string) *tar.Header {
		h := stableTarHeader
		h.Name = name
		h.Typeflag = tar.TypeReg
		return &h
	}
	want := must(archivetest.TarFile([]archive.TarEntry{
		{Header: stableOuterEntry("data.tar.gz"), Body: must(archivetest.GzFile(
			must(archivetest.TarFile([]archive.TarEntry{
				{Header: stableOuterEntry("lib/foo.rb"), Body: []byte("puts 'foo'\n")},
			})).Bytes(),
			stableGzipHeader, gzip.NoCompression,
		)).Bytes()},
		{Header: stableOuterEntry("metadata.gz"), Body: must(archivetest.GzFile(
			metaContent,
			stableGzipHeader, gzip.NoCompression,
		)).Bytes()},
	}))

	allStabs := append(append(append([]Stabilizer{}, AllTarStabilizers...), AllGzipStabilizers...), AllGemStabilizers...)
	var got bytes.Buffer
	orDie(StabilizeWithOpts(&got, bytes.NewReader(input.Bytes()), archive.TarFormat, StabilizeOpts{Stabilizers: allStabs}))

	if diff := cmp.Diff(want.Bytes(), got.Bytes()); diff != "" {
		t.Errorf("StabilizeWithOpts() output mismatch (-want +got):\n%s", diff)
	}
}

func TestStableGemIdempotent(t *testing.T) {
	nonEpoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	gemTar := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.TgzFile([]archive.TarEntry{
			{Header: &tar.Header{Name: "lib/foo.rb", Typeflag: tar.TypeReg, ModTime: nonEpoch}, Body: []byte("hello")},
		})).Bytes()},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.GzFile([]byte("name: foo\n"), gzip.Header{Name: "metadata", ModTime: nonEpoch})).Bytes()},
		{Header: &tar.Header{Name: "checksums.yaml.gz", Typeflag: tar.TypeReg}, Body: []byte("sums")},
	}))

	allStabs := append(append(append([]Stabilizer{}, AllTarStabilizers...), AllGzipStabilizers...), AllGemStabilizers...)
	opts := StabilizeOpts{Stabilizers: allStabs}

	var pass1 bytes.Buffer
	orDie(StabilizeWithOpts(&pass1, bytes.NewReader(gemTar.Bytes()), archive.TarFormat, opts))

	var pass2 bytes.Buffer
	orDie(StabilizeWithOpts(&pass2, bytes.NewReader(pass1.Bytes()), archive.TarFormat, opts))

	if !bytes.Equal(pass1.Bytes(), pass2.Bytes()) {
		t.Error("stabilization is not idempotent: pass1 != pass2")
	}
}
