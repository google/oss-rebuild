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
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/pkg/errors"
)

const maxELFFileSize = 128 << 20 // 128 MiB

var (
	// sharedLibNamePat matches shared object filenames (.so or .so.<numeric-version>).
	sharedLibNamePat = re.MustCompile(`\.so(?:\.\d+)*$`)
	// auditwheel inserts an 8+ character hex hash right before the first dot in
	// the grafted shared library filename (e.g. libcgraph-17278668.so.6.0.0 -> libcgraph.so.6.0.0).
	auditwheelHashPat = re.MustCompile(`^([^.]+)-[0-9a-fA-F]{8,}\.`)
	// auditwheel names grafted files after the resolved symlink target, whereas
	// package managers index the SONAME, which carries only the major version (e.g. libcgraph.so.6.0.0 -> libcgraph.so.6).
	sonameFullVersionPat = re.MustCompile(`^(.+\.so\.\d+)\.\d+\.\d+$`)
	// Core C/C++/Fortran and GCC runtime libraries pre-installed in PyPA manylinux
	// and musllinux build containers that should not be installed as external packages.
	// NOTE: This intentionally omits non-core PEP 513/599 allowlisted libraries
	// such as libGL and libX11 so their development headers are still installed
	// when an extension links against them.
	builtinLibraries = []string{
		"ld64.so.1",
		"ld64.so.2",
		"linux-vdso.so.1",
		"libc.so.6",
		"libm.so.6",
		"libmvec.so.1",
		"libpthread.so.0",
		"libdl.so.2",
		"librt.so.1",
		"libutil.so.1",
		"libresolv.so.2",
		"libnsl.so.1",
		"libcrypt.so.1",
		"libcrypt.so.2",
		"libanl.so.1",
		"libBrokenLocale.so.1",
		"libcidn.so.1",
		"libmemusage.so",
		"libpcprofile.so",
		"libSegFault.so",
		"libthread_db.so.1",
		"libatomic.so.1",
		"libstdc++.so.6",
		"libgcc_s.so.1",
		"libgomp.so.1",
		"libquadmath.so.0",
		"libgfortran.so.3",
		"libgfortran.so.4",
		"libgfortran.so.5",
	}
	builtinLibPrefixes = []string{"ld-linux", "ld-musl-", "libc.musl-", "libnss_", "libpython"}
)

func isBuiltinLibrary(soname string) bool {
	return slices.Contains(builtinLibraries, soname) ||
		slices.ContainsFunc(builtinLibPrefixes, func(p string) bool { return strings.HasPrefix(soname, p) })
}

// libraryStem extracts the C library stem from a SONAME (e.g. "libcgraph.so.6" -> "cgraph").
func libraryStem(soname string) string {
	if !strings.HasPrefix(soname, "lib") || strings.Contains(soname, ".cpython-") || strings.Contains(soname, ".pypy") || strings.HasSuffix(soname, ".abi3.so") {
		return ""
	}
	if idx := strings.LastIndex(soname, ".so"); idx > 3 {
		return soname[3:idx]
	}
	return ""
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
// package specifiers use pc:lib<stem> because apk does not index arbitrary
// header or linker symlink file paths.
func formatLibraryDeps(soname string, libc platform.LibcImpl) []string {
	stem := libraryStem(soname)
	if stem == "" {
		return nil
	}
	switch libc {
	case platform.Musl:
		return []string{"so:" + soname, "pc:lib" + stem}
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
		if f.FileInfo().IsDir() || f.UncompressedSize64 > maxELFFileSize {
			continue
		}
		baseName := path.Base(f.Name)
		if !sharedLibNamePat.MatchString(strings.ToLower(baseName)) {
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
		ef, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			continue
		}
		cand := baseName
		if sonames, err := ef.DynString(elf.DT_SONAME); err == nil && len(sonames) > 0 && sonames[0] != "" {
			cand = sonames[0]
		}
		if isBundledLibPath(f.Name) {
			rawCandidates = append(rawCandidates, cand)
		} else {
			internalLibs[unbundledSoname(baseName)] = true
			internalLibs[unbundledSoname(cand)] = true
		}
		if imported, err := ef.ImportedLibraries(); err == nil {
			rawCandidates = append(rawCandidates, imported...)
		}
	}
	var deps []string
	for _, raw := range rawCandidates {
		soname := unbundledSoname(raw)
		if soname == "" || internalLibs[soname] || isBuiltinLibrary(soname) {
			continue
		}
		deps = appendUnique(deps, formatLibraryDeps(soname, libc)...)
	}
	return deps, nil
}

func appendUnique(dst []string, src ...string) []string {
	for _, s := range src {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}
