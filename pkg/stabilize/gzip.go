// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"compress/gzip"
	"io"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
)

// AllGzipStabilizers is the list of all available gzip stabilizers.
var AllGzipStabilizers = []Stabilizer{
	StableGzipCompression,
	StableGzipName,
	StableGzipTime,
	StableGzipMisc,
}

// defaultCompression enables us to configure the default compression value
// used in tests.
var defaultCompression = gzip.DefaultCompression

// NewStabilizedGzipWriter applies the provided stabilizers to the gzip.Reader metadata.
// The caller is responsible for closing the returned writer.
//
// NOTE: This abstraction differs from the other stabilizers because the
// compression level used in the gzip.Writer is not configurable after
// construction. As a result, a raw writer must be provided and a gzip.Writer
// returned to ensure a configurable compression level.
func NewStabilizedGzipWriter(gr *gzip.Reader, w io.Writer, opts StabilizeOpts) (*gzip.Writer, error) {
	header := gr.Header
	mh := archive.MutableGzipHeader{Header: &header, Compression: defaultCompression}
	for _, s := range opts.Stabilizers {
		switch s.(type) {
		case GzipStabilizer:
			s.(GzipStabilizer).Stabilize(&mh)
		}
	}
	gw, err := gzip.NewWriterLevel(w, mh.Compression)
	if err != nil {
		return nil, err
	}
	gw.Header = *mh.Header
	return gw, nil
}

// GzipStabilizer applies stabilization to gzip headers.
type GzipStabilizer struct {
	Name string
	Func func(*archive.MutableGzipHeader)
}

// Stabilize applies the stabilizer function to the given MutableGzipHeader.
func (g GzipStabilizer) Stabilize(arg any) {
	g.Func(arg.(*archive.MutableGzipHeader))
}

// StableGzipCompression sets compression to no compression.
var StableGzipCompression = GzipStabilizer{
	Name: "gzip-compression",
	Func: func(h *archive.MutableGzipHeader) {
		h.Compression = gzip.NoCompression
	},
}

// StableGzipName clears the filename.
var StableGzipName = GzipStabilizer{
	Name: "gzip-name",
	Func: func(h *archive.MutableGzipHeader) {
		h.Name = ""
	},
}

// StableGzipTime zeroes out modification time.
var StableGzipTime = GzipStabilizer{
	Name: "gzip-time",
	Func: func(h *archive.MutableGzipHeader) {
		// NOTE: time.Time{} can be round-tripped more cleanly than the epoch value
		// because, per the spec, the field is not serialized when set to the zero
		// value. As a result, writing the epoch would be read back as time.Time{}.
		h.ModTime = time.Time{}
	},
}

// StableGzipMisc clears miscellaneous metadata fields.
var StableGzipMisc = GzipStabilizer{
	Name: "gzip-misc",
	Func: func(h *archive.MutableGzipHeader) {
		h.Comment = ""
		h.Extra = nil
		h.OS = 255 // unknown
	},
}
