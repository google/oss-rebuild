// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
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

// CanonicalizeZip strips volatile metadata and rewrites the provided archive in a canonical form.
func CanonicalizeZip(zr *zip.Reader, zw *zip.Writer) error {
	defer zw.Close()
	var ents []ZipEntry
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(r)
		if err != nil {
			r.Close()
			return err
		}
		if err := r.Close(); err != nil {
			return err
		}
		// TODO: Memory-intensive. We're buffering the full file in memory (again).
		// One option would be to do two passes and only buffer what's necessary.
		ents = append(ents, ZipEntry{&zip.FileHeader{Name: f.Name, Modified: time.UnixMilli(0)}, b})
	}
	sort.Slice(ents, func(i, j int) bool {
		return ents[i].FileHeader.Name < ents[j].FileHeader.Name
	})
	for _, ent := range ents {
		w, err := zw.CreateHeader(ent.FileHeader)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, bytes.NewReader(ent.Body)); err != nil {
			return err
		}
	}
	return nil
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
