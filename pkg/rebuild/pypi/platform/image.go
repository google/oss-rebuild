// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import "github.com/pkg/errors"

// The PyPA build image repositories that SelectBaseImage returns.
// NOTE: gen_cibuildwheel_images.go lists these by name to generate the
// cibuildwheel pin table. Keep its supportedRepos in sync.
const (
	ImageManylinux2014X86_64 = "quay.io/pypa/manylinux2014_x86_64"
	ImageManylinux2_28X86_64 = "quay.io/pypa/manylinux_2_28_x86_64"
	ImageManylinux2_34X86_64 = "quay.io/pypa/manylinux_2_34_x86_64"

	ImageMusllinux1_1X86_64 = "quay.io/pypa/musllinux_1_1_x86_64"
	ImageMusllinux1_2X86_64 = "quay.io/pypa/musllinux_1_2_x86_64"
)

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
