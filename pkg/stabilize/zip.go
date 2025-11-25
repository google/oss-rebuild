// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/zip"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/archive"
)

// AllZipStabilizers is the list of all available zip stabilizers.
var AllZipStabilizers = []Stabilizer{
	StableZipFileOrder,
	StableZipModifiedTime,
	StableZipCompression,
	StableZipDataDescriptor,
	StableZipFileEncoding,
	StableZipFileMode,
	StableZipMisc,
}

// ZipArchiveStabilizer applies stabilization to an entire zip archive.
type ZipArchiveStabilizer struct {
	Name string
	Func func(*archive.MutableZipReader)
}

// Stabilize applies the stabilizer function to the given MutableZipReader.
func (z ZipArchiveStabilizer) Stabilize(arg any) {
	z.Func(arg.(*archive.MutableZipReader))
}

// ZipEntryStabilizer applies stabilization to individual zip entries.
type ZipEntryStabilizer struct {
	Name string
	Func func(*archive.MutableZipFile)
}

// Stabilize applies the stabilizer function to the given MutableZipFile.
func (z ZipEntryStabilizer) Stabilize(arg any) {
	z.Func(arg.(*archive.MutableZipFile))
}

// StableZipFileOrder sorts zip entries by name.
var StableZipFileOrder = ZipArchiveStabilizer{
	Name: "zip-file-order",
	Func: func(zr *archive.MutableZipReader) {
		slices.SortFunc(zr.File, func(i, j *archive.MutableZipFile) int {
			return strings.Compare(i.Name, j.Name)
		})
	},
}

// StableZipModifiedTime zeroes out modification times.
var StableZipModifiedTime = ZipEntryStabilizer{
	Name: "zip-modified-time",
	Func: func(zf *archive.MutableZipFile) {
		zf.Modified = time.UnixMilli(0)
		zf.ModifiedDate = 0
		zf.ModifiedTime = 0
	},
}

// StableZipCompression sets compression to Store (uncompressed).
var StableZipCompression = ZipEntryStabilizer{
	Name: "zip-compression",
	Func: func(zf *archive.MutableZipFile) {
		zf.Method = zip.Store
	},
}

var dataDescriptorFlag = uint16(0x8)

// StableZipDataDescriptor clears data descriptor flags and related fields.
var StableZipDataDescriptor = ZipEntryStabilizer{
	Name: "zip-data-descriptor",
	Func: func(zf *archive.MutableZipFile) {
		zf.Flags = zf.Flags & ^dataDescriptorFlag
		zf.CRC32 = 0
		zf.CompressedSize = 0
		zf.CompressedSize64 = 0
		zf.UncompressedSize = 0
		zf.UncompressedSize64 = 0
	},
}

// StableZipFileEncoding clears the non-UTF8 flag.
var StableZipFileEncoding = ZipEntryStabilizer{
	Name: "zip-file-encoding",
	Func: func(zf *archive.MutableZipFile) {
		zf.NonUTF8 = false
	},
}

// StableZipFileMode clears creator version and external attributes.
var StableZipFileMode = ZipEntryStabilizer{
	Name: "zip-file-mode",
	Func: func(zf *archive.MutableZipFile) {
		zf.CreatorVersion = 0
		zf.ExternalAttrs = 0
	},
}

// StableZipMisc clears miscellaneous metadata fields.
var StableZipMisc = ZipEntryStabilizer{
	Name: "zip-misc",
	Func: func(zf *archive.MutableZipFile) {
		zf.Comment = ""
		zf.ReaderVersion = 0
		zf.Extra = []byte{}
		// NOTE: Zero all flags except the data descriptor one handled above.
		zf.Flags = zf.Flags & dataDescriptorFlag
	},
}

// StabilizeZip strips volatile metadata and rewrites the provided archive in a standard form.
func StabilizeZip(zr *zip.Reader, zw *zip.Writer, opts StabilizeOpts) error {
	defer zw.Close()
	var headers []zip.FileHeader
	for _, zf := range zr.File {
		headers = append(headers, zf.FileHeader)
	}
	mr := archive.NewMutableReader(zr)
	for _, s := range opts.Stabilizers {
		switch s.(type) {
		case ZipArchiveStabilizer:
			s.(ZipArchiveStabilizer).Stabilize(&mr)
		case ZipEntryStabilizer:
			for _, mf := range mr.File {
				s.(ZipEntryStabilizer).Stabilize(mf)
			}
		}
	}
	return mr.WriteTo(zw)
}
