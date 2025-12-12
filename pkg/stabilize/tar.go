// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"encoding/json"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/hash"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

// AllTarStabilizers is the list of all available tar stabilizers.
var AllTarStabilizers = []Stabilizer{
	StableTarFileOrder,
	StableTarTime,
	StableTarFileMode,
	StableTarOwners,
	StableTarXattrs,
	StableTarDeviceNumber,
}

// TarArchiveStabilizer applies stabilization to an entire tar archive.
type TarArchiveStabilizer struct {
	Name string
	Func func(*archive.TarArchive)
}

// Stabilize applies the stabilizer function to the given TarArchive.
func (t TarArchiveStabilizer) Stabilize(arg any) {
	t.Func(arg.(*archive.TarArchive))
}

// TarEntryStabilizer applies stabilization to individual tar entries.
type TarEntryStabilizer struct {
	Name string
	Func func(*archive.TarEntry)
}

// Stabilize applies the stabilizer function to the given TarEntry.
func (t TarEntryStabilizer) Stabilize(arg any) {
	t.Func(arg.(*archive.TarEntry))
}

// StableTarFileOrder sorts tar entries by name.
var StableTarFileOrder = TarArchiveStabilizer{
	Name: "tar-file-order",
	Func: func(f *archive.TarArchive) {
		slices.SortFunc(f.Files, func(a, b *archive.TarEntry) int {
			return strings.Compare(a.Name, b.Name)
		})
	},
}

// StableTarTime zeroes out timestamps.
var StableTarTime = TarEntryStabilizer{
	Name: "tar-time",
	Func: func(e *archive.TarEntry) {
		e.ModTime = time.UnixMilli(0)
		e.AccessTime = time.UnixMilli(0)
		e.ChangeTime = time.Time{}
		// NOTE: Without a PAX record, the tar library will disregard this value
		// and write the format as USTAR. Setting 'atime' ensures at least one
		// PAX record exists which will cause tar to be always be considered a PAX.
		e.Format = tar.FormatPAX
	},
}

// StableTarFileMode sets file mode to default permissions.
var StableTarFileMode = TarEntryStabilizer{
	Name: "tar-file-mode",
	Func: func(e *archive.TarEntry) {
		e.Mode = int64(fs.ModePerm)
	},
}

// StableTarOwners clears owner information.
var StableTarOwners = TarEntryStabilizer{
	Name: "tar-owners",
	Func: func(e *archive.TarEntry) {
		e.Uid = 0
		e.Gid = 0
		e.Uname = ""
		e.Gname = ""
	},
}

// StableTarXattrs clears extended attributes and PAX records.
var StableTarXattrs = TarEntryStabilizer{
	Name: "tar-xattrs",
	Func: func(e *archive.TarEntry) {
		clear(e.Xattrs)
		clear(e.PAXRecords)
	},
}

// StableTarDeviceNumber clears device numbers.
var StableTarDeviceNumber = TarEntryStabilizer{
	Name: "tar-device-number",
	Func: func(e *archive.TarEntry) {
		// NOTE: 0 is currently reserved on Linux and will dynamically allocate a
		// device number when passed to the kernel.
		e.Devmajor = 0
		e.Devminor = 0
	},
}

// AllCrateStabilizers is the list of all available crate stabilizers.
var AllCrateStabilizers = []Stabilizer{
	StabilizeCargoVCSHash,
}

// StabilizeCargoVCSHash normalizes the VCS hash in cargo_vcs_info.json files.
var StabilizeCargoVCSHash = TarEntryStabilizer{
	Name: "cargo-vcs-hash",
	Func: func(e *archive.TarEntry) {
		if strings.HasSuffix(e.Name, ".cargo_vcs_info.json") {
			var vcsInfo map[string]any
			if err := json.Unmarshal(e.Body, &vcsInfo); err != nil {
				return // Skip if invalid JSON
			}
			if git, ok := vcsInfo["git"].(map[string]any); ok {
				if _, hasSha1 := git["sha1"]; hasSha1 {
					git["sha1"] = strings.Repeat("x", hash.HexSize)
					if newBody, err := json.Marshal(vcsInfo); err == nil {
						e.Body = newBody
						e.Size = int64(len(newBody))
					}
				}
			}
		}
	},
}

// StabilizeTar strips volatile metadata and re-writes the provided archive in a standard form.
func StabilizeTar(tr *tar.Reader, tw *tar.Writer, opts StabilizeOpts) error {
	defer tw.Close()
	var ents []*archive.TarEntry
	for header, err := range iterx.ToSeq2(tr, io.EOF) {
		if err != nil {
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
		ents = append(ents, &archive.TarEntry{Header: header, Body: buf[:]})
	}
	f := archive.TarArchive{Files: ents}
	for _, s := range opts.Stabilizers {
		switch s.(type) {
		case TarArchiveStabilizer:
			s.(TarArchiveStabilizer).Stabilize(&f)
		case TarEntryStabilizer:
			for _, ent := range f.Files {
				s.(TarEntryStabilizer).Stabilize(ent)
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
