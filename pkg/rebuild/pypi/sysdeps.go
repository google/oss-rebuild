// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"path"
	re "regexp"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/pkg/errors"
)

const maxELFFileSize = 128 << 20 // 128 MiB

var (
	// sharedLibNamePat matches shared object filenames (.so or .so.<numeric-version>).
	sharedLibNamePat = re.MustCompile(`\.so(?:\.\d+)*$`)
	// auditwheel inserts an 8+ character hex hash right before the first dot in
	// the grafted shared library filename.
	auditwheelHashPat = re.MustCompile(`^([^.]+)-[0-9a-fA-F]{8,}\.`)
	// auditwheel names grafted files after the resolved symlink target (such as
	// libcgraph-17278668.so.6.0.0), whereas package manager shared object name
	// (SONAME) provides use the major version (libcgraph.so.6).
	sonameFullVersionPat = re.MustCompile(`^(.+\.so\.\d+)\.\d+\.\d+$`)
)

// Core C/C++ and GCC runtime libraries pre-installed in PyPA manylinux and
// musllinux build containers that should not be installed as external packages.
// NOTE: This intentionally omits non-core PEP 513/599 allowlisted libraries
// such as libGL and libX11 so their development headers are still installed
// when an extension links against them.
var standardLibraries = map[string]bool{
	"ld64.so.1":            true,
	"ld64.so.2":            true,
	"linux-vdso.so.1":      true,
	"libc.so.6":            true,
	"libm.so.6":            true,
	"libmvec.so.1":         true,
	"libpthread.so.0":      true,
	"libdl.so.2":           true,
	"librt.so.1":           true,
	"libutil.so.1":         true,
	"libresolv.so.2":       true,
	"libnsl.so.1":          true,
	"libcrypt.so.1":        true,
	"libcrypt.so.2":        true,
	"libanl.so.1":          true,
	"libBrokenLocale.so.1": true,
	"libcidn.so.1":         true,
	"libmemusage.so":       true,
	"libpcprofile.so":      true,
	"libSegFault.so":       true,
	"libthread_db.so.1":    true,
	"libatomic.so.1":       true,
	"libstdc++.so.6":       true,
	"libgcc_s.so.1":        true,
	"libgomp.so.1":         true,
	"libquadmath.so.0":     true,
}

func isStandardLibrary(soname string) bool {
	if standardLibraries[soname] {
		return true
	}
	for _, prefix := range []string{"ld-linux", "ld-musl-", "libc.musl-", "libnss_", "libpython"} {
		if strings.HasPrefix(soname, prefix) {
			return true
		}
	}
	return false
}

// libraryStem extracts the C library stem from a SONAME (e.g. "libcgraph.so.6" -> "cgraph").
func libraryStem(soname string) string {
	if !strings.HasPrefix(soname, "lib") {
		return ""
	}
	if strings.Contains(soname, ".cpython-") || strings.Contains(soname, ".pypy") || strings.HasSuffix(soname, ".abi3.so") {
		return ""
	}
	idx := strings.LastIndex(soname, ".so")
	if idx <= 3 {
		return ""
	}
	return soname[3:idx]
}

func isBundledLibPath(p string) bool {
	base := path.Base(path.Dir(p))
	return strings.HasSuffix(base, ".libs") || strings.HasPrefix(base, ".libs_")
}

// unbundledSoname strips auditwheel hash suffixes and normalizes 3-part file
// versions back to the major SONAME.
func unbundledSoname(name string) string {
	unbundled := auditwheelHashPat.ReplaceAllString(name, "$1.")
	return sonameFullVersionPat.ReplaceAllString(unbundled, "$1")
}

// formatLibraryDeps formats a normalized SONAME into runtime and development
// package manager specifiers for the target libc family.
// NOTE: Glibc specifiers assume a 64-bit target architecture, matching
// platform.SelectBaseImage which currently supports x86_64. Musl development
// package specifiers use pc:lib<stem> and <stem>-dev as best-effort heuristics
// because apk does not index arbitrary header or linker symlink file paths.
func formatLibraryDeps(soname string, libc platform.LibcImpl) []string {
	stem := libraryStem(soname)
	if stem == "" {
		return nil
	}
	switch libc {
	case platform.Musl:
		return []string{"so:" + soname, "pc:lib" + stem, stem + "-dev"}
	case platform.Glibc:
		return []string{soname + "()(64bit)", "/usr/lib64/lib" + stem + ".so"}
	default:
		panic(fmt.Sprintf("unexpected libc implementation %q", libc))
	}
}

// inferELFSystemDeps extracts shared library dependencies from Executable and
// Linkable Format (ELF) shared objects in a wheel archive and formats them for
// the target libc's package manager.
func inferELFSystemDeps(zr *zip.Reader, libc platform.LibcImpl) ([]string, error) {
	var rawCandidates []string
	internalLibs := make(map[string]bool)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		baseName := path.Base(f.Name)
		if !sharedLibNamePat.MatchString(strings.ToLower(baseName)) {
			continue
		}
		bundled := isBundledLibPath(f.Name)
		if !bundled {
			internalLibs[unbundledSoname(baseName)] = true
		}
		if f.UncompressedSize64 > maxELFFileSize {
			if bundled {
				rawCandidates = append(rawCandidates, baseName)
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.Wrapf(err, "opening %s in wheel", f.Name)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, errors.Wrapf(err, "reading %s in wheel", f.Name)
		}
		elfFile, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			if bundled {
				rawCandidates = append(rawCandidates, baseName)
			}
			continue
		}
		sonames, err := elfFile.DynString(elf.DT_SONAME)
		hasSoname := err == nil && len(sonames) > 0 && sonames[0] != ""
		if bundled {
			if hasSoname {
				rawCandidates = append(rawCandidates, sonames[0])
			} else {
				rawCandidates = append(rawCandidates, baseName)
			}
		} else if hasSoname {
			internalLibs[unbundledSoname(sonames[0])] = true
		}
		if imported, err := elfFile.ImportedLibraries(); err == nil {
			rawCandidates = append(rawCandidates, imported...)
		}
		elfFile.Close()
	}
	var deps []string
	for _, raw := range rawCandidates {
		soname := unbundledSoname(raw)
		if soname == "" || internalLibs[soname] || isStandardLibrary(soname) {
			continue
		}
		deps = append(deps, formatLibraryDeps(soname, libc)...)
	}
	return deduplicateSystemDeps(deps), nil
}

func deduplicateSystemDeps(deps []string) []string {
	var out []string
	seen := make(map[string]bool, len(deps))
	for _, dep := range deps {
		if !seen[dep] {
			seen[dep] = true
			out = append(out, dep)
		}
	}
	return out
}
