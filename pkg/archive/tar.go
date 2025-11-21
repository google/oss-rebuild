// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v5"
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

// TarArchive represents a collection of tar entries.
type TarArchive struct {
	Files []*TarEntry
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
