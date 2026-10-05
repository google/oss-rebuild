// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

const (
	ImageManylinux2014X86_64 = "quay.io/pypa/manylinux2014_x86_64"
	ImageManylinux2_28X86_64 = "quay.io/pypa/manylinux_2_28_x86_64"
	ImageManylinux2_34X86_64 = "quay.io/pypa/manylinux_2_34_x86_64"

	ImageMusllinux1_1X86_64 = "quay.io/pypa/musllinux_1_1_x86_64"
	ImageMusllinux1_2X86_64 = "quay.io/pypa/musllinux_1_2_x86_64"
)

var (
	redHatCommentRe = regexp.MustCompile(`\(Red Hat (\d+)\.(\d+)\.`)
	alpineCommentRe = regexp.MustCompile(`\(Alpine ((\d+)\.[^)]+)\)`)
)

// DetectBaseImage inspects ELF .comment sections of shared objects in a wheel archive
// to identify the PyPA container image used to build the wheel. It returns an empty
// string if no shared objects contain a recognized container toolchain signature.
//
// Platform tags alone cannot always identify the build container. When a wheel is built
// inside a newer container (such as manylinux_2_28) and its compiled shared objects only
// reference <= GLIBC_2.17 symbols, passing --plat manylinux2014_x86_64 to auditwheel
// repair (or building with tools like maturin that emit only the lowest satisfied policy)
// produces a filename tagged solely manylinux2014_x86_64.manylinux_2_17_x86_64. In those
// cases the filename tag hides the actual build container, which causes rebuilds to run
// with the wrong GCC version or fail when dependencies require a newer compiler.
//
// Even when auditwheel repair --strip (or strip --strip-unneeded) removes symbol tables
// and debug sections, the ELF .comment section is preserved and records the compiler and
// distro strings from the build environment. Extension modules outside *.libs/ are
// inspected before bundled shared libraries inside *.libs/ because auditwheel copies
// external or prebuilt third-party libraries into *.libs/, whereas the Python extension
// module itself is compiled and linked directly inside the build container.
func DetectBaseImage(zr *zip.Reader) string {
	if zr == nil {
		return ""
	}
	var bundled []*zip.File
	for _, zf := range zr.File {
		if !isSharedObject(zf.Name) {
			continue
		}
		if isBundledLib(zf.Name) {
			bundled = append(bundled, zf)
			continue
		}
		if img := detectFileBaseImage(zf); img != "" {
			return img
		}
	}
	for _, zf := range bundled {
		if img := detectFileBaseImage(zf); img != "" {
			return img
		}
	}
	return ""
}

func isSharedObject(name string) bool {
	base := path.Base(name)
	return strings.HasSuffix(base, ".so") || strings.Contains(base, ".so.")
}

func isBundledLib(name string) bool {
	for _, part := range strings.Split(path.Dir(name), "/") {
		if strings.HasSuffix(part, ".libs") {
			return true
		}
	}
	return false
}

func detectFileBaseImage(zf *zip.File) string {
	rc, err := zf.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	ef, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	defer ef.Close()
	if ef.Machine != elf.EM_X86_64 {
		return ""
	}
	sec := ef.Section(".comment")
	if sec == nil {
		return ""
	}
	raw, err := sec.Data()
	if err != nil {
		return ""
	}
	return baseImageFromCommentSection(raw)
}

// baseImageFromCommentSection maps NUL-separated ELF .comment strings to a PyPA image.
//
// In Red Hat-family containers (CentOS and AlmaLinux), linking a shared object against
// glibc merges .comment entries from two sources:
//  1. The base OS glibc startup objects (crti.o and crtn.o), which were compiled with the
//     distro's system GCC (4.4 on CentOS 6, 4.8 on CentOS 7, 8.4/8.5 on AlmaLinux 8, and
//     11.3+ on AlmaLinux 9).
//  2. The devtoolset or gcc-toolset compiler used to build the wheel's C/C++ objects and
//     crtbeginS.o (devtoolset 8/9/10/11 on CentOS 7, gcc-toolset 11/12/13/14 on AlmaLinux 8,
//     and gcc-toolset 12/13/14 on AlmaLinux 9).
//
// Because gcc-toolset 12, 13, and 14 are used on both AlmaLinux 8 (manylinux_2_28) and
// AlmaLinux 9 (manylinux_2_34), the toolset version alone does not uniquely identify the
// container. We therefore match the base OS crti.o compiler signature first across all
// entries before falling back to the devtoolset/gcc-toolset version (for binaries linked
// with -nostartfiles).
//
// In Alpine containers (musllinux), a single system GCC builds both musl and user code:
// Alpine 3.12 through 3.19 (musllinux_1_1) ship GCC 9 through 13.2.1_git2023*, whereas
// Alpine 3.20+ (musllinux_1_2) ships GCC 13.2.1_git2024* and GCC 14+.
func baseImageFromCommentSection(raw []byte) string {
	var redHatVersions []Version
	for _, entry := range strings.Split(string(raw), "\x00") {
		if m := alpineCommentRe.FindStringSubmatch(entry); m != nil {
			ver := m[1]
			major, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			if major >= 14 || strings.HasPrefix(ver, "13.2.1_git2024") || strings.HasPrefix(ver, "13.2.1_git2025") {
				return ImageMusllinux1_2X86_64
			}
			if major >= 8 {
				return ImageMusllinux1_1X86_64
			}
		}
		if m := redHatCommentRe.FindStringSubmatch(entry); m != nil {
			major, err1 := strconv.Atoi(m[1])
			minor, err2 := strconv.Atoi(m[2])
			if err1 == nil && err2 == nil {
				redHatVersions = append(redHatVersions, Version{Major: major, Minor: minor})
			}
		}
	}
	// Pass 1: Match the base OS system GCC recorded by crti.o/crtn.o.
	// CentOS 6 (4.4) and CentOS 7 (4.8) map to manylinux2014.
	for _, v := range redHatVersions {
		if v.Major == 4 {
			return ImageManylinux2014X86_64
		}
	}
	// AlmaLinux 8 system GCC (8.4 and 8.5, distinct from devtoolset-8's 8.3 on CentOS 7)
	// maps to manylinux_2_28.
	for _, v := range redHatVersions {
		if v.Major == 8 && (v.Minor == 4 || v.Minor == 5) {
			return ImageManylinux2_28X86_64
		}
	}
	// AlmaLinux 9 system GCC (11.3+, distinct from devtoolset-11/gcc-toolset-11's 11.2)
	// maps to manylinux_2_34.
	for _, v := range redHatVersions {
		if v.Major == 11 && v.Minor >= 3 {
			return ImageManylinux2_34X86_64
		}
	}
	// Pass 2: Fallback for binaries linked without crti.o (-nostartfiles), where only the
	// devtoolset/gcc-toolset version is present.
	for _, v := range redHatVersions {
		if v.Major <= 10 {
			return ImageManylinux2014X86_64
		}
		return ImageManylinux2_28X86_64
	}
	return ""
}

// SelectBaseImage selects the appropriate PyPA container image for a given platform tag
// with support for PEP 425 single and dot-compressed tag sets.
//
// This selection is based on two rules:
//
//  1. Highest tag in a compressed set: Official PyPA images set AUDITWHEEL_PLAT to the
//     container's own policy. When a wheel compiled in a newer container (such as
//     manylinux_2_28) only imports symbols from an older libc (such as <= GLIBC_2.17),
//     auditwheel _prepends_ the older compatible policy tags to the container's policy tag.
//     The other way around, when a wheel was built in an older container like manylinux2014,
//     auditwheel doesn't by default append those. Selecting the highest libc version tag to select
//     the build container thus leads to the correct choice in the standard case.
//
//  2. Rounding intermediate libc versions up: Only three active glibc PyPA images exist
//     (manylinux2014 at glibc 2.17, manylinux_2_28 at glibc 2.28, and manylinux_2_34 at
//     glibc 2.34). When a wheel carries an intermediate tag (such as manylinux_2_24 or
//     manylinux_2_31), rounding down to an older container fails if the build requires symbols or
//     headers from that intermediate glibc version, and auditwheel inside an older container
//     refuses to target a --plat policy newer than the container's own glibc. Because glibc
//     versions symbols individually (GLIBC_x.y) rather than enforcing a global library version
//     check, rounding up to the smallest PyPA image whose glibc >= the target tag provides all
//     required symbols and allows auditwheel repair to target the older policy.
//
// Resulting x86_64 mappings:
//   - manylinux 2.5 <= glibc <= 2.17:  quay.io/pypa/manylinux2014_x86_64 (CentOS 7, glibc 2.17)
//   - manylinux 2.18 <= glibc <= 2.28: quay.io/pypa/manylinux_2_28_x86_64 (AlmaLinux 8, glibc 2.28)
//   - manylinux glibc >= 2.29:         quay.io/pypa/manylinux_2_34_x86_64 (AlmaLinux 9, glibc 2.34)
//   - musllinux 1.1 <= musl < 1.2:     quay.io/pypa/musllinux_1_1_x86_64 (Alpine 3.12+, musl 1.1)
//   - musllinux musl >= 1.2:           quay.io/pypa/musllinux_1_2_x86_64 (Alpine 3.20+, musl 1.2)
func SelectBaseImage(platformTags string) (string, error) {
	tags, err := ParsePlatformTags(platformTags)
	if err != nil {
		return "", errors.Wrapf(err, "parsing platform tags %q", platformTags)
	}
	highest, err := HighestLibcVersionTag(tags)
	if err != nil {
		return "", errors.Wrapf(err, "determining highest libc version tag for %q", platformTags)
	}
	if highest.Arch != "x86_64" {
		return "", errors.Errorf("unsupported architecture %q in platform tag %q", highest.Arch, platformTags)
	}
	switch highest.LibcImpl {
	case Musl:
		// PEP 656 defines musl 1.1 as the minimum musllinux version.
		if highest.LibcVersion.Less(Version{Major: 1, Minor: 1}) {
			return "", errors.Errorf("unsupported musl version %s in platform tag %q", highest.LibcVersion, platformTags)
		}
		if highest.LibcVersion.Less(Version{Major: 1, Minor: 2}) {
			return ImageMusllinux1_1X86_64, nil
		}
		return ImageMusllinux1_2X86_64, nil
	case Glibc:
		// PEP 513 (manylinux1) defines glibc 2.5 as the minimum manylinux version.
		if highest.LibcVersion.Less(Version{Major: 2, Minor: 5}) {
			return "", errors.Errorf("unsupported glibc version %s in platform tag %q", highest.LibcVersion, platformTags)
		}
		// Legacy tags <= 2.17 (manylinux1, manylinux2010, manylinux2014) map to manylinux2014,
		// the oldest maintained PyPA container image.
		if highest.LibcVersion.Less(Version{Major: 2, Minor: 18}) {
			return ImageManylinux2014X86_64, nil
		}
		// Intermediate tags in 2.18..2.28 (such as manylinux_2_24) round up to manylinux_2_28
		// so the container's glibc and auditwheel support the requested policy ceiling.
		if highest.LibcVersion.Less(Version{Major: 2, Minor: 29}) {
			return ImageManylinux2_28X86_64, nil
		}
		// Tags >= 2.29 (such as manylinux_2_31 and manylinux_2_34) map to manylinux_2_34,
		// the newest available PyPA container image.
		return ImageManylinux2_34X86_64, nil
	default:
		return "", errors.Errorf("unknown libc family %q in platform tag %q", highest.LibcImpl, platformTags)
	}
}
