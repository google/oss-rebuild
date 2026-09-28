// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

var auditwheelHashPattern = regexp.MustCompile(`-[0-9a-fA-F]{8,}\.so`)
var sonameMajorPattern = regexp.MustCompile(`^(lib[a-zA-Z0-9_\-+]+)\.so\.(\d+)`)

// ExtractWheelElfDependencies extracts shared library sonames and C library stems from ELF binaries inside a wheel.
func ExtractWheelElfDependencies(zr *zip.Reader) ([]DependencyIdentifier, error) {
	var ids []DependencyIdentifier
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".so") && !strings.Contains(f.Name, ".so.") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.Wrapf(err, "opening file %s in wheel", f.Name)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, errors.Wrapf(err, "reading file %s in wheel", f.Name)
		}

		// auditwheel appends hashes to .so filenames in `.libs/` directory, but we want the "pure"
		// names as they would appear on a host system
		if strings.Contains(f.Name, ".libs/") {
			base := filepath.Base(f.Name)
			unhashed := auditwheelHashPattern.ReplaceAllString(base, ".so")
			if soname := extractSonameFromFilename(unhashed); soname != "" && !isStandardBaseLibrary(soname) {
				ids = append(ids, DependencyIdentifier{
					Namespace:  NamespaceSoname,
					Name:       soname,
					Provenance: "wheel:libs:" + f.Name,
				})
			}
			if stem := extractCLibStem(unhashed); stem != "" {
				ids = append(ids, DependencyIdentifier{
					Namespace:  NamespaceCLib,
					Name:       stem,
					Provenance: "wheel:libs:" + f.Name,
				})
			}
		}
		elfFile, err := elf.NewFile(bytes.NewReader(body))
		if err != nil {
			// Skip non-ELF files or architecture mismatches
			continue
		}
		// Extract all the libraries that the shared object expresses to depend on
		libs, err := elfFile.ImportedLibraries()
		if err != nil {
			continue
		}
		for _, lib := range libs {
			if isStandardBaseLibrary(lib) {
				continue
			}
			ids = append(ids, DependencyIdentifier{
				Namespace:  NamespaceSoname,
				Name:       lib,
				Provenance: "wheel:dt_needed:" + f.Name,
			})
			if stem := extractCLibStem(lib); stem != "" {
				ids = append(ids, DependencyIdentifier{
					Namespace:  NamespaceCLib,
					Name:       stem,
					Provenance: "wheel:dt_needed:" + f.Name,
				})
			}
		}
	}
	return DeduplicateIdentifiers(ids), nil
}

// Determine whether a library is a base library already installed in the build images
func isStandardBaseLibrary(soname string) bool {
	switch {
	case soname == "libc.so.6",
		soname == "libm.so.6",
		soname == "libdl.so.2",
		soname == "libpthread.so.0",
		soname == "librt.so.1",
		soname == "libutil.so.1",
		soname == "libresolv.so.2",
		soname == "libnsl.so.1",
		soname == "libcrypt.so.1",
		soname == "libgcc_s.so.1",
		soname == "libstdc++.so.6",
		strings.HasPrefix(soname, "ld-linux"),
		strings.HasPrefix(soname, "ld-musl"),
		strings.HasPrefix(soname, "libpython"):
		return true
	}
	return false
}

func extractCLibStem(name string) string {
	cleaned := auditwheelHashPattern.ReplaceAllString(name, ".so")
	base := filepath.Base(cleaned)
	if !strings.HasPrefix(base, "lib") {
		return ""
	}
	stem := strings.TrimPrefix(base, "lib")
	idx := strings.Index(stem, ".so")
	if idx <= 0 {
		return ""
	}
	return stem[:idx]
}

func extractSonameFromFilename(name string) string {
	cleaned := auditwheelHashPattern.ReplaceAllString(name, ".so")
	base := filepath.Base(cleaned)
	if m := sonameMajorPattern.FindStringSubmatch(base); len(m) == 3 {
		return m[1] + ".so." + m[2]
	}
	if strings.HasSuffix(base, ".so") {
		return base
	}
	return ""
}
