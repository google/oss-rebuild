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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	billy "github.com/go-git/go-billy/v5"
	"github.com/pkg/errors"
)

// Pick some arbitrary time to set all the time fields.
// Source: https://github.com/npm/pacote/blob/main/lib/util/tar-create-options.js#L28
var arbitraryTime = time.Date(1985, time.October, 26, 8, 15, 0, 0, time.UTC)

func canonicalizeTarHeader(h *tar.Header) (*tar.Header, error) {
	switch h.Typeflag {
	case tar.TypeGNUSparse, tar.TypeGNULongName, tar.TypeGNULongLink:
		// NOTE: Non-PAX header type support can be added, if necessary.
		return nil, errors.Errorf("Unsupported header type: %v", h.Typeflag)
	default:
		return &tar.Header{
			Typeflag:   h.Typeflag,
			Name:       h.Name,
			ModTime:    arbitraryTime,
			AccessTime: arbitraryTime,
			// TODO: Surface presence/absence of execute bit as a comparison config.
			Mode:  0777,
			Uid:   0,
			Gid:   0,
			Uname: "",
			Gname: "",
			Size:  h.Size,
			// TODO: Surface comparison config for TAR metadata (PAXRecords, Xattrs).
			Format: tar.FormatPAX,
		}, nil
	}
}

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

// CanonicalizeTar strips volatile metadata and re-writes the provided archive in a canonical form.
func CanonicalizeTar(tr *tar.Reader, tw *tar.Writer) error {
	defer tw.Close()
	var ents []TarEntry
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break // End of archive
			}
			return err
		}
		canonicalized, err := canonicalizeTarHeader(header)
		if err != nil {
			return err
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		// TODO: Memory-intensive. We're buffering the full file in memory (again).
		// One option would be to do two passes and only buffer what's necessary.
		ents = append(ents, TarEntry{canonicalized, buf[:]})
	}
	sort.Slice(ents, func(i, j int) bool {
		return ents[i].Header.Name < ents[j].Header.Name
	})
	for _, ent := range ents {
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
