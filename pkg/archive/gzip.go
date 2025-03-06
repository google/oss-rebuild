// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"compress/gzip"
	"io"
	"time"
)

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
	mh := MutableGzipHeader{Header: &header, Compression: defaultCompression}
	for _, s := range opts.Stabilizers {
		switch s.(type) {
		case GzipStabilizer:
			s.(GzipStabilizer).Func(&mh)
		}
	}
	gw, err := gzip.NewWriterLevel(w, mh.Compression)
	if err != nil {
		return nil, err
	}
	gw.Header = *mh.Header
	return gw, nil
}

type MutableGzipHeader struct {
	*gzip.Header
	Compression int
}

type GzipStabilizer struct {
	Name string
	Func func(*MutableGzipHeader)
}

var AllGzipStabilizers = []Stabilizer{
	StableGzipCompression,
	StableGzipName,
	StableGzipTime,
	StableGzipMisc,
}

var StableGzipCompression = GzipStabilizer{
	Name: "gzip-compression",
	Func: func(h *MutableGzipHeader) {
		h.Compression = gzip.NoCompression
	},
}

var StableGzipName = GzipStabilizer{
	Name: "gzip-name",
	Func: func(h *MutableGzipHeader) {
		h.Name = ""
	},
}

var StableGzipTime = GzipStabilizer{
	Name: "gzip-time",
	Func: func(h *MutableGzipHeader) {
		// NOTE: time.Time{} can be round-tripped more cleanly than the epoch value
		// because, per the spec, the field is not serialized when set to the zero
		// value. As a result, writing the epoch would be read back as time.Time{}.
		h.ModTime = time.Time{}
	},
}

var StableGzipMisc = GzipStabilizer{
	Name: "gzip-misc",
	Func: func(h *MutableGzipHeader) {
		h.Comment = ""
		h.Extra = nil
		h.OS = 255 // unknown
	},
}
