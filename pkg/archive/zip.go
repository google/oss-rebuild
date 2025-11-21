// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"

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
		h := sha256.Sum256(buf)
		cs.FileHashes = append(cs.FileHashes, hex.EncodeToString(h[:]))
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

// Open returns a reader for the file content.
func (mf *MutableZipFile) Open() (io.Reader, error) {
	if mf.mutContent != nil {
		return bytes.NewReader(mf.mutContent), nil
	}
	return mf.File.Open()
}

// SetContent sets the modified content for this file.
func (mf *MutableZipFile) SetContent(content []byte) {
	mf.mutContent = content
}

// MutableZipReader wraps zip.Reader to allow in-place modification of the original.
type MutableZipReader struct {
	*zip.Reader
	File    []*MutableZipFile
	Comment string
}

// NewMutableReader creates a MutableZipReader from a zip.Reader.
func NewMutableReader(zr *zip.Reader) MutableZipReader {
	mr := MutableZipReader{Reader: zr}
	mr.Comment = mr.Reader.Comment
	for _, zf := range zr.File {
		mr.File = append(mr.File, &MutableZipFile{File: zf, FileHeader: zf.FileHeader})
	}
	return mr
}

// WriteTo writes the modified archive to a zip writer.
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

// ToZipCompatibleReader coerces an io.Reader into an io.ReaderAt required to construct a zip.Reader.
func ToZipCompatibleReader(r io.Reader) (io.ReaderAt, int64, error) {
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
