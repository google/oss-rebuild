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
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	billy "github.com/go-git/go-billy/v5"
	"github.com/pkg/errors"
)

// TarEntry represents an entry in a tar archive.
type TarEntry struct {
	*tar.Header
	Body []byte
}

// WriteTo writes the TarEntry to a tar writer.
func (e TarEntry) WriteTo(tw *tar.Writer) error {
	if err := tw.WriteHeader(e.Header); err != nil {
		return err
	}
	if _, err := tw.Write(e.Body); err != nil {
		return err
	}
	return nil
}

type TarFile struct{ Files []*TarEntry }

type TarArchiveSanitizer struct {
	Name string
	Func func(*TarFile)
}

type TarEntrySanitizer struct {
	Name string
	Func func(*TarEntry)
}

var AllTarSanitizers []any = []any{
	NormTarFileOrder,
	NormTarTime,
	NormTarFileMode,
	NormTarOwners,
	NormTarXattrs,
	NormTarDeviceNumber,
}

var NormTarFileOrder = TarArchiveSanitizer{
	Name: "tar-file-order",
	Func: func(f *TarFile) {
		slices.SortFunc(f.Files, func(a, b *TarEntry) int {
			return strings.Compare(a.Name, b.Name)
		})
	},
}

var NormTarTime = TarEntrySanitizer{
	Name: "tar-time",
	Func: func(e *TarEntry) {
		e.ModTime = time.UnixMilli(0)
		e.AccessTime = time.UnixMilli(0)
		e.ChangeTime = time.Time{}
		// NOTE: Without a PAX record, the tar library will disregard this value
		// and write a USTAR-formatted file. Setting 'atime' ensures at least one
		// record exists which will cause tar to serialize and re-parse it as PAX.
		e.Format = tar.FormatPAX
	},
}

var NormTarFileMode = TarEntrySanitizer{
	Name: "tar-file-mode",
	Func: func(e *TarEntry) {
		e.Mode = int64(fs.ModePerm)
	},
}

var NormTarOwners = TarEntrySanitizer{
	Name: "tar-owners",
	Func: func(e *TarEntry) {
		e.Uid = 0
		e.Gid = 0
		e.Uname = ""
		e.Gname = ""
	},
}

var NormTarXattrs = TarEntrySanitizer{
	Name: "tar-xattrs",
	Func: func(e *TarEntry) {
		clear(e.Xattrs)
		clear(e.PAXRecords)
	},
}

var NormTarDeviceNumber = TarEntrySanitizer{
	Name: "tar-device-number",
	Func: func(e *TarEntry) {
		// NOTE: 0 is currently reserved on Linux and will dynamically allocated a
		// device number when passed to the kernel.
		e.Devmajor = 0
		e.Devminor = 0
	},
}

// CanonicalizeTar strips volatile metadata and re-writes the provided archive in a canonical form.
func CanonicalizeTar(tr *tar.Reader, tw *tar.Writer, opts CanonicalizeOpts) error {
	defer tw.Close()
	var ents []*TarEntry
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break // End of archive
			}
			return err
		}
		// NOTE: Non-PAX header type support can be added, if necessary.
		switch header.Typeflag {
		case tar.TypeGNUSparse, tar.TypeGNULongName, tar.TypeGNULongLink:
			return errors.New("Unsupported file type")
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		// NOTE: Memory-intensive. We're buffering the full file in memory as
		// tar.Reader is single-pass and we need to support sorting entries.
		ents = append(ents, &TarEntry{header, buf[:]})
	}
	f := TarFile{Files: ents}
	for _, s := range opts.Sanitizers {
		switch s.(type) {
		case TarArchiveSanitizer:
			s.(TarArchiveSanitizer).Func(&f)
		case TarEntrySanitizer:
			for _, ent := range f.Files {
				s.(TarEntrySanitizer).Func(ent)
			}
		}
	}
	for _, ent := range f.Files {
		if err := ent.WriteTo(tw); err != nil {
			return err
		}
	}
	return nil
}

// ExtractOptions provides options modifying ExtractTar behavior.
type ExtractOptions struct {
	// SubDir is a directory within the TAR to extract relative to the provided filesystem.
	SubDir string
}

// ExtractTar writes the contents of a tar to a filesystem.
func ExtractTar(tr *tar.Reader, fs billy.Filesystem, opt ExtractOptions) error {
	basepath := filepath.Clean(opt.SubDir) + string(filepath.Separator)
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		path, err := filepath.Rel(basepath, h.Name)
		if err != nil {
			return err
		}
		skip := slices.Contains(strings.Split(path, string(filepath.Separator)), "..")
		if h.Linkname != "" {
			linkpath, err := filepath.Rel(basepath, h.Linkname)
			if err != nil {
				return err
			}
			if err := fs.Symlink(linkpath, path); err != nil {
				return err
			}
		} else if h.FileInfo().IsDir() {
			if skip {
				continue
			}
			if err := fs.MkdirAll(path, h.FileInfo().Mode()); err != nil {
				return err
			}
		} else {
			if skip {
				if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
					return err
				}
				continue
			}
			tf, err := fs.OpenFile(path, os.O_WRONLY|os.O_CREATE, h.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.CopyN(tf, tr, h.Size); err != nil {
				return err
			}
			if err := tf.Close(); err != nil {
				return err
			}
		}
	}
}

// NewContentSummaryFromTar returns a ContentSummary for a tar archive.
func NewContentSummaryFromTar(tr *tar.Reader) (*ContentSummary, error) {
	cs := ContentSummary{
		Files:      make([]string, 0),
		FileHashes: make([]string, 0),
		CRLFCount:  0,
	}
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break // End of archive
			}
			return nil, errors.Wrap(err, "failed to read tar header")
		}
		switch header.Typeflag {
		case tar.TypeGNUSparse, tar.TypeGNULongName, tar.TypeGNULongLink:
			// NOTE: Non-PAX header type support can be added, if necessary.
			return nil, errors.Errorf("Unsupported header type: %v", header.Typeflag)
		default:
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read tar entry %s", header.Name)
		}
		cs.Files = append(cs.Files, header.Name)
		cs.CRLFCount += bytes.Count(buf, []byte{'\r', '\n'})
		cs.FileHashes = append(cs.FileHashes, hex.EncodeToString(sha256.New().Sum(buf)))
	}
	return &cs, nil
}
