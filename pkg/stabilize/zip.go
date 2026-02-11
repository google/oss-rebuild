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

var zipFormats = []archive.Format{archive.ZipFormat}

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

// ZipArchiveFn applies stabilization to an entire zip archive.
type ZipArchiveFn func(*archive.MutableZipReader)

func (ZipArchiveFn) Constraints() Constraints {
	return []Constraint{Formats(zipFormats)}
}

// ZipEntryFn applies stabilization to a single zip entry.
type ZipEntryFn func(*archive.MutableZipFile)

func (ZipEntryFn) Constraints() Constraints {
	return []Constraint{Formats(zipFormats)}
}

// StableZipFileOrder sorts zip entries by name.
var StableZipFileOrder = Stabilizer{
	Name: "zip-file-order",
}.WithFn(ZipArchiveFn(func(zr *archive.MutableZipReader) {
	slices.SortFunc(zr.File, func(i, j *archive.MutableZipFile) int {
		return strings.Compare(i.Name, j.Name)
	})
}))

// StableZipModifiedTime zeroes out timestamps.
var StableZipModifiedTime = Stabilizer{
	Name: "zip-modified-time",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.Modified = time.UnixMilli(0)
	zf.ModifiedDate = 0
	zf.ModifiedTime = 0
}))

// StableZipCompression sets compression to Store (uncompressed).
var StableZipCompression = Stabilizer{
	Name: "zip-compression",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.Method = zip.Store
}))

var dataDescriptorFlag = uint16(0x8)

// StableZipDataDescriptor clears data descriptor flags and related fields.
var StableZipDataDescriptor = Stabilizer{
	Name: "zip-data-descriptor",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.Flags = zf.Flags & ^dataDescriptorFlag
	zf.CRC32 = 0
	zf.CompressedSize = 0
	zf.CompressedSize64 = 0
	zf.UncompressedSize = 0
	zf.UncompressedSize64 = 0
}))

// StableZipFileEncoding clears the non-UTF8 flag.
var StableZipFileEncoding = Stabilizer{
	Name: "zip-file-encoding",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.NonUTF8 = false
}))

// StableZipFileMode clears creator version and external attributes.
var StableZipFileMode = Stabilizer{
	Name: "zip-file-mode",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.CreatorVersion = 0
	zf.ExternalAttrs = 0
}))

// StableZipMisc clears miscellaneous metadata fields.
var StableZipMisc = Stabilizer{
	Name: "zip-misc",
}.WithFn(ZipEntryFn(func(zf *archive.MutableZipFile) {
	zf.Comment = ""
	zf.ReaderVersion = 0
	zf.Extra = []byte{}
	// NOTE: Zero all flags except the data descriptor one handled above.
	zf.Flags = zf.Flags & dataDescriptorFlag
}))

// StabilizeZip strips volatile metadata and rewrites the provided archive in a standard form.
func StabilizeZip(zr *zip.Reader, zw *zip.Writer, opts StabilizeOpts, ctx *StabilizationContext) error {
	defer zw.Close()
	mr := archive.NewMutableReader(zr)
	// TODO: This ordering is inefficient as it lacks reuse for entryCtx
	for _, s := range opts.Stabilizers {
		if fn, ok := s.FnFor(ctx).(ZipArchiveFn); ok && fn != nil {
			fn(&mr)
		} else {
			for _, mf := range mr.File {
				entryCtx := ctx.WithEntry(mf.Name)
				if fn, ok := s.FnFor(entryCtx).(ZipEntryFn); ok {
					fn(mf)
				}
			}
		}
	}
	return mr.WriteTo(zw)
}
