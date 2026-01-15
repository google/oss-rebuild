// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
)

// xflMap associates gzip compression constants with XFL compression level flag.
// Source: https://datatracker.ietf.org/doc/html/rfc1952#:~:text=XFL%20(eXtra%20FLags)
var xflMap = map[int]byte{
	gzip.BestCompression:    0x2,
	gzip.BestSpeed:          0x4,
	gzip.DefaultCompression: 0x0,
	gzip.HuffmanOnly:        0x0,
	gzip.NoCompression:      0x0,
}

func TestNewStabilizedGzipWriter(t *testing.T) {
	tests := []struct {
		name               string
		input              []byte
		header             gzip.Header
		compression        int
		defaultCompression int
		stabilizers        []Stabilizer
		wantHeader         gzip.Header
		wantCompression    int
	}{
		{
			name:  "all stabilizers",
			input: []byte("hello world"),
			header: gzip.Header{
				Name:    "test.txt",
				Comment: "test comment",
				ModTime: time.UnixMilli(1000).UTC(),
				Extra:   []byte("extra"),
				OS:      3,
			},
			compression: gzip.BestCompression,
			stabilizers: AllGzipStabilizers,
			wantHeader: gzip.Header{
				Name:    "",
				Comment: "",
				ModTime: time.Time{},
				Extra:   nil,
				OS:      255,
			},
			wantCompression:    gzip.NoCompression,
			defaultCompression: gzip.BestSpeed,
		},
		{
			name:  "only name stabilizer",
			input: []byte("hello world"),
			header: gzip.Header{
				Name:    "test.txt",
				Comment: "test comment",
				ModTime: time.UnixMilli(1000).UTC(),
				Extra:   []byte("extra"),
				OS:      3,
			},
			compression: gzip.BestSpeed,
			stabilizers: []Stabilizer{StableGzipName},
			wantHeader: gzip.Header{
				Name:    "",
				Comment: "test comment",
				ModTime: time.UnixMilli(1000).UTC(),
				Extra:   []byte("extra"),
				OS:      3,
			},
			wantCompression:    gzip.DefaultCompression,
			defaultCompression: gzip.DefaultCompression,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure compression assertions are possible
			if tt.defaultCompression != tt.wantCompression && xflMap[tt.defaultCompression] == xflMap[tt.wantCompression] {
				t.Errorf("cannot differentiate default versus target compression schemes")
			}
			defaultCompression = tt.defaultCompression

			// Create input buffer with gzip data
			inBuf := &bytes.Buffer{}
			gw := must(gzip.NewWriterLevel(inBuf, tt.compression))
			gw.Header = tt.header
			must(gw.Write(tt.input))
			orDie(gw.Close())

			if xflMap[tt.compression] != inBuf.Bytes()[8] {
				t.Errorf("compression mismatch: got %x, want %x", inBuf.Bytes()[8], xflMap[tt.compression])
			}

			// Apply stabilization to gzip data
			outBuf := &bytes.Buffer{}
			gr := must(gzip.NewReader(bytes.NewReader(inBuf.Bytes())))
			gw = must(NewStabilizedGzipWriter(gr, outBuf, StabilizeOpts{Stabilizers: tt.stabilizers}, NewContext(archive.GzipFormat)))
			must(io.Copy(gw, gr))
			orDie(gw.Close())

			// Verify output
			grr := must(gzip.NewReader(bytes.NewReader(outBuf.Bytes())))
			if diff := cmp.Diff(tt.wantHeader, grr.Header, cmp.AllowUnexported(time.Time{})); diff != "" {
				t.Errorf("header mismatch (-want +got):\n%s", diff)
			}
			content := must(io.ReadAll(grr))
			if !bytes.Equal(content, tt.input) {
				t.Errorf("content mismatch: got %q, want %q", content, tt.input)
			}
			if xflMap[tt.wantCompression] != outBuf.Bytes()[8] {
				t.Errorf("compression mismatch: got %x, want %x", outBuf.Bytes()[8], xflMap[tt.wantCompression])
			}
		})
	}
}
