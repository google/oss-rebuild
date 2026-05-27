// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/diffr/diffrtest"
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
	if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
		t.Errorf("StabilizeTar() mismatch (-want +got):\n%s", diff)
	}
}

func TestStableGemExcludeSignatures(t *testing.T) {
	input := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: []byte("data")},
		{Header: &tar.Header{Name: "data.tar.gz.sig", Typeflag: tar.TypeReg}, Body: []byte("dsig")},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: []byte("meta")},
		{Header: &tar.Header{Name: "metadata.gz.sig", Typeflag: tar.TypeReg}, Body: []byte("msig")},
		{Header: &tar.Header{Name: "checksums.yaml.gz.sig", Typeflag: tar.TypeReg}, Body: []byte("csig")},
	}))
	want := must(archivetest.TarFile([]archive.TarEntry{
		{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: []byte("data")},
		{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: []byte("meta")},
	}))
	var got bytes.Buffer
	orDie(StabilizeTar(
		tar.NewReader(bytes.NewReader(input.Bytes())),
		tar.NewWriter(&got),
		NewContext(archive.TarFormat).WithStabilizers([]Stabilizer{StableGemExcludeSignatures}),
	))
	if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
		t.Errorf("StabilizeTar() mismatch (-want +got):\n%s", diff)
	}
}

func TestStableGemMetadataCertChain(t *testing.T) {
	libBody := []byte("hello")
	stableEntry := func(name string) *tar.Header {
		h := stableTarHeader
		h.Name = name
		h.Typeflag = tar.TypeReg
		return &h
	}
	allStabs := append(append(append([]Stabilizer{}, AllTarStabilizers...), AllGzipStabilizers...), AllGemStabilizers...)

	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "strips list-form cert_chain",
			in:   "name: foo\ncert_chain:\n- |\n  -----BEGIN CERTIFICATE-----\n  MIIDxxx\n  -----END CERTIFICATE-----\nemail: a@b.c\n",
			want: "name: foo\ncert_chain: []\nemail: a@b.c\n",
		},
		{
			name: "strips indented cert_chain block",
			in:   "name: foo\ncert_chain:\n  some: value\n  other: value\nemail: a@b.c\n",
			want: "name: foo\ncert_chain: []\nemail: a@b.c\n",
		},
		{
			name: "no-op when cert_chain absent",
			in:   "name: foo\nemail: a@b.c\n",
			want: "name: foo\nemail: a@b.c\n",
		},
		{
			name: "leaves indented (nested) cert_chain structurally untouched",
			in:   "outer:\n  cert_chain:\n  - value\n",
			want: "outer:\n  cert_chain:\n    - value\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := must(archivetest.TarFile([]archive.TarEntry{
				{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.TgzFile([]archive.TarEntry{
					{Header: &tar.Header{Name: "lib/foo.rb", Typeflag: tar.TypeReg}, Body: libBody},
				})).Bytes()},
				{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.GzFile([]byte(tc.in), gzip.Header{})).Bytes()},
				{Header: &tar.Header{Name: "checksums.yaml.gz", Typeflag: tar.TypeReg}, Body: []byte("sums")},
			}))
			want := must(archivetest.TarFile([]archive.TarEntry{
				{Header: stableEntry("data.tar.gz"), Body: must(archivetest.GzFile(
					must(archivetest.TarFile([]archive.TarEntry{
						{Header: stableEntry("lib/foo.rb"), Body: libBody},
					})).Bytes(),
					stableGzipHeader, gzip.NoCompression,
				)).Bytes()},
				{Header: stableEntry("metadata.gz"), Body: must(archivetest.GzFile(
					[]byte(tc.want), stableGzipHeader, gzip.NoCompression,
				)).Bytes()},
			}))
			var got bytes.Buffer
			orDie(StabilizeWithOpts(&got, bytes.NewReader(input.Bytes()), archive.TarFormat, StabilizeOpts{Stabilizers: allStabs}))
			if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
				t.Errorf("StabilizeWithOpts() output mismatch (-want +got):\n%s", diff)
			}
		})
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

	if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
		t.Errorf("StabilizeWithOpts() output mismatch (-want +got):\n%s", diff)
	}
}

func TestStableGemMetadata(t *testing.T) {
	libBody := []byte("hello")
	stableEntry := func(name string) *tar.Header {
		h := stableTarHeader
		h.Name = name
		h.Typeflag = tar.TypeReg
		return &h
	}
	allStabs := append(append(append([]Stabilizer{}, AllTarStabilizers...), AllGzipStabilizers...), AllGemStabilizers...)

	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "rewrites top-level date and rubygems_version",
			in:   "name: foo\ndate: 2024-06-15 00:00:00.000000000 Z\nrubygems_version: 3.5.11\n",
			want: "name: foo\ndate: 1980-01-02 00:00:00.000000000 Z\nrubygems_version: 0.0.0\n",
		},
		{
			// CRLF in metadata is rare in practice (Psych/libyaml emit LF on
			// every platform). The YAML round-trip normalizes line endings to
			// LF, which is fine and matches real-world output.
			name: "normalizes CRLF terminator to LF",
			in:   "date: 2024-06-15 00:00:00.000000000 Z\r\n",
			want: "date: 1980-01-02 00:00:00.000000000 Z\n",
		},
		{
			name: "leaves indented (nested) date untouched",
			in:   "dependencies:\n  date: 2020-01-01\n",
			want: "dependencies:\n  date: 2020-01-01\n",
		},
		{
			name: "no-op when target fields absent",
			in:   "name: foo\n",
			want: "name: foo\n",
		},
		{
			name: "flow-style array normalized to block style",
			in:   "executables: [bin/a, bin/b, bin/c]\n",
			want: "executables:\n  - bin/a\n  - bin/b\n  - bin/c\n",
		},
		{
			name: "empty flow array preserved as []",
			in:   "cert_chain: []\n",
			want: "cert_chain: []\n",
		},
		{
			name: "4-space indent normalized to 2-space",
			in:   "metadata:\n    bug_tracker_uri: https://x\n    homepage_uri: https://y\n",
			want: "metadata:\n  bug_tracker_uri: https://x\n  homepage_uri: https://y\n",
		},
		{
			name: "double-quoted scalar unquoted when safe",
			in:   "name: \"foo\"\n",
			want: "name: foo\n",
		},
		{
			name: "single-quoted scalar unquoted when safe",
			in:   "name: 'foo'\n",
			want: "name: foo\n",
		},
		{
			name: "mixed array quote styles normalize together",
			in:   "authors: [\"alice\", 'bob', carol]\n",
			want: "authors:\n  - alice\n  - bob\n  - carol\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := must(archivetest.TarFile([]archive.TarEntry{
				{Header: &tar.Header{Name: "data.tar.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.TgzFile([]archive.TarEntry{
					{Header: &tar.Header{Name: "lib/foo.rb", Typeflag: tar.TypeReg}, Body: libBody},
				})).Bytes()},
				{Header: &tar.Header{Name: "metadata.gz", Typeflag: tar.TypeReg}, Body: must(archivetest.GzFile([]byte(tc.in), gzip.Header{})).Bytes()},
				{Header: &tar.Header{Name: "checksums.yaml.gz", Typeflag: tar.TypeReg}, Body: []byte("sums")},
			}))
			want := must(archivetest.TarFile([]archive.TarEntry{
				{Header: stableEntry("data.tar.gz"), Body: must(archivetest.GzFile(
					must(archivetest.TarFile([]archive.TarEntry{
						{Header: stableEntry("lib/foo.rb"), Body: libBody},
					})).Bytes(),
					stableGzipHeader, gzip.NoCompression,
				)).Bytes()},
				{Header: stableEntry("metadata.gz"), Body: must(archivetest.GzFile(
					[]byte(tc.want), stableGzipHeader, gzip.NoCompression,
				)).Bytes()},
			}))
			var got bytes.Buffer
			orDie(StabilizeWithOpts(&got, bytes.NewReader(input.Bytes()), archive.TarFormat, StabilizeOpts{Stabilizers: allStabs}))
			if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
				t.Errorf("StabilizeWithOpts() output mismatch (-want +got):\n%s", diff)
			}
		})
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

	if diff := diffrtest.Diff(t, pass1.Bytes(), pass2.Bytes()); diff != "" {
		t.Errorf("stabilization is not idempotent (pass1 vs pass2):\n%s", diff)
	}
}
