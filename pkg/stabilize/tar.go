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

var tarFormats = []archive.Format{archive.TarFormat, archive.TarGzFormat}

// AllTarStabilizers is the list of all available tar stabilizers.
var AllTarStabilizers = []Stabilizer{
	StableTarFileOrder,
	StableTarTime,
	StableTarFileMode,
	StableTarOwners,
	StableTarXattrs,
	StableTarDeviceNumber,
}

// TarArchiveFn applies stabilization to an entire tar archive.
type TarArchiveFn func(*archive.TarArchive)

func (TarArchiveFn) Constraints() Constraints {
	return []Constraint{Formats(tarFormats)}
}

// TarEntryFn applies stabilization to a single tar entry.
type TarEntryFn func(*archive.TarEntry)

func (TarEntryFn) Constraints() Constraints {
	return []Constraint{Formats(tarFormats)}
}

// StableTarFileOrder sorts tar entries by name.
var StableTarFileOrder = Stabilizer{
	Name: "tar-file-order",
}.WithFn(TarArchiveFn(func(f *archive.TarArchive) {
	slices.SortFunc(f.Files, func(a, b *archive.TarEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
}))

// StableTarTime zeroes out timestamps.
var StableTarTime = Stabilizer{
	Name: "tar-time",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
	e.ModTime = time.UnixMilli(0)
	e.AccessTime = time.UnixMilli(0)
	e.ChangeTime = time.Time{}
	// NOTE: Without a PAX record, the tar library will disregard this value
	// and write the format as USTAR. Setting 'atime' ensures at least one
	// PAX record exists which will cause tar to be always be considered a PAX.
	e.Format = tar.FormatPAX
}))

// StableTarFileMode sets file mode to default permissions.
var StableTarFileMode = Stabilizer{
	Name: "tar-file-mode",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
	e.Mode = int64(fs.ModePerm)
}))

// StableTarOwners clears owner information.
var StableTarOwners = Stabilizer{
	Name: "tar-owners",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
	e.Uid = 0
	e.Gid = 0
	e.Uname = ""
	e.Gname = ""
}))

// StableTarXattrs clears extended attributes and PAX records.
var StableTarXattrs = Stabilizer{
	Name: "tar-xattrs",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
	clear(e.Xattrs)
	clear(e.PAXRecords)
}))

// StableTarDeviceNumber clears device numbers.
var StableTarDeviceNumber = Stabilizer{
	Name: "tar-device-number",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
	// NOTE: 0 is currently reserved on Linux and will dynamically allocate a
	// device number when passed to the kernel.
	e.Devmajor = 0
	e.Devminor = 0
}))

// AllCrateStabilizers is the list of all available crate stabilizers.
var AllCrateStabilizers = []Stabilizer{
	StabilizeCargoVCSHash,
}

// StabilizeCargoVCSHash normalizes the VCS hash in cargo_vcs_info.json files.
var StabilizeCargoVCSHash = Stabilizer{
	Name: "cargo-vcs-hash",
}.WithFn(TarEntryFn(func(e *archive.TarEntry) {
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
}))

// StabilizeTar strips volatile metadata and re-writes the provided archive in a standard form.
func StabilizeTar(tr *tar.Reader, tw *tar.Writer, opts StabilizeOpts, ctx *StabilizationContext) error {
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
	// TODO: This ordering is inefficient as it lacks reuse for entryCtx
	for _, s := range opts.Stabilizers {
		if fn, ok := s.FnFor(ctx).(TarArchiveFn); ok && fn != nil {
			fn(&f)
		} else {
			for _, ent := range f.Files {
				entryCtx := ctx.WithEntry(ent.Name)
				if fn, ok := s.FnFor(entryCtx).(TarEntryFn); ok {
					fn(ent)
				}
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
