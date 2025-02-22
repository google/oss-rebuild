// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// NewContentSummaryFromZip returns a ContentSummary for a zip archive.
func NewContentSummaryFromZip(zr *zip.Reader) (*ContentSummary, error) {
	cs := ContentSummary{
		Files:      make([]string, 0),
		FileHashes: make([]string, 0),
		CRLFCount:  0,
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		buf, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		cs.Files = append(cs.Files, f.Name)
		cs.CRLFCount += bytes.Count(buf, []byte{'\r', '\n'})
		cs.FileHashes = append(cs.FileHashes, hex.EncodeToString(sha256.New().Sum(buf)))
	}
	return &cs, nil
}

// ZipEntry represents an entry in a zip archive.
// TODO: Move to archivetest.
type ZipEntry struct {
	*zip.FileHeader
	Body []byte
}

// WriteTo writes the ZipEntry to a zip writer.
func (e ZipEntry) WriteTo(zw *zip.Writer) error {
	fw, err := zw.CreateHeader(e.FileHeader)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, bytes.NewReader(e.Body)); err != nil {
		return err
	}
	return nil
}

// MutableZipFile wraps zip.File to allow in-place modification of the original.
type MutableZipFile struct {
	zip.FileHeader
	File       *zip.File
	mutContent []byte
}

func (mf *MutableZipFile) Open() (io.Reader, error) {
	if mf.mutContent != nil {
		return bytes.NewReader(mf.mutContent), nil
	}
	return mf.File.Open()
}

func (mf *MutableZipFile) SetContent(content []byte) {
	mf.mutContent = content
}

// MutableZipReader wraps zip.Reader to allow in-place modification of the original.
type MutableZipReader struct {
	*zip.Reader
	File    []*MutableZipFile
	Comment string
}

func NewMutableReader(zr *zip.Reader) MutableZipReader {
	mr := MutableZipReader{Reader: zr}
	mr.Comment = mr.Reader.Comment
	for _, zf := range zr.File {
		mr.File = append(mr.File, &MutableZipFile{File: zf, FileHeader: zf.FileHeader})
	}
	return mr
}

func (mr MutableZipReader) WriteTo(zw *zip.Writer) error {
	if err := zw.SetComment(mr.Comment); err != nil {
		return err
	}
	for _, mf := range mr.File {
		r, err := mf.Open()
		if err != nil {
			return err
		}
		w, err := zw.CreateHeader(&mf.FileHeader)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			return err
		}
	}
	return nil
}

type ZipArchiveStabilizer struct {
	Name string
	Func func(*MutableZipReader)
}

type ZipEntryStabilizer struct {
	Name string
	Func func(*MutableZipFile)
}

var AllZipStabilizers []any = []any{
	StableZipFileOrder,
	StableZipModifiedTime,
	StableZipCompression,
	StableZipDataDescriptor,
	StableZipFileEncoding,
	StableZipFileMode,
	StableZipMisc,
}

var StableZipFileOrder = ZipArchiveStabilizer{
	Name: "zip-file-order",
	Func: func(zr *MutableZipReader) {
		slices.SortFunc(zr.File, func(i, j *MutableZipFile) int {
			return strings.Compare(i.Name, j.Name)
		})
	},
}

var StableZipModifiedTime = ZipEntryStabilizer{
	Name: "zip-modified-time",
	Func: func(zf *MutableZipFile) {
		zf.Modified = time.UnixMilli(0)
		zf.ModifiedDate = 0
		zf.ModifiedTime = 0
	},
}

var StableZipCompression = ZipEntryStabilizer{
	Name: "zip-compression",
	Func: func(zf *MutableZipFile) {
		zf.Method = zip.Store
	},
}

var dataDescriptorFlag = uint16(0x8)

var StableZipDataDescriptor = ZipEntryStabilizer{
	Name: "zip-data-descriptor",
	Func: func(zf *MutableZipFile) {
		zf.Flags = zf.Flags & ^dataDescriptorFlag
		zf.CRC32 = 0
		zf.CompressedSize = 0
		zf.CompressedSize64 = 0
		zf.UncompressedSize = 0
		zf.UncompressedSize64 = 0
	},
}

var StableZipFileEncoding = ZipEntryStabilizer{
	Name: "zip-file-encoding",
	Func: func(zf *MutableZipFile) {
		zf.NonUTF8 = false
	},
}

var StableZipFileMode = ZipEntryStabilizer{
	Name: "zip-file-mode",
	Func: func(zf *MutableZipFile) {
		zf.CreatorVersion = 0
		zf.ExternalAttrs = 0
	},
}

var StableZipMisc = ZipEntryStabilizer{
	Name: "zip-misc",
	Func: func(zf *MutableZipFile) {
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
	mr := NewMutableReader(zr)
	for _, s := range opts.Stabilizers {
		switch s.(type) {
		case ZipArchiveStabilizer:
			s.(ZipArchiveStabilizer).Func(&mr)
		case ZipEntryStabilizer:
			for _, mf := range mr.File {
				s.(ZipEntryStabilizer).Func(mf)
			}
		}
	}
	return mr.WriteTo(zw)
}

// toZipCompatibleReader coerces an io.Reader into an io.ReaderAt required to construct a zip.Reader.
func toZipCompatibleReader(r io.Reader) (io.ReaderAt, int64, error) {
	seeker, seekerOK := r.(io.Seeker)
	readerAt, readerOK := r.(io.ReaderAt)
	if seekerOK && readerOK {
		pos, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, errors.Wrap(err, "locating reader position")
		}
		size, err := seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, 0, errors.Wrap(err, "retrieving size")
		}
		if _, err := seeker.Seek(pos, io.SeekStart); err != nil {
			return nil, 0, errors.Wrap(err, "restoring reader position")
		}
		return readerAt, size, nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, errors.New("unsupported reader")
	}
	return bytes.NewReader(b), int64(len(b)), nil
}
