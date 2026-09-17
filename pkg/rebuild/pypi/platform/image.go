// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import "github.com/pkg/errors"

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
// Because libc versions are forward-compatible but not backward-compatible, this selects
// an image whose libc version is less than or equal to the lowest target tag version:
// For x86_64 manylinux:
// - 2.5 <= glibc < 2.28:  quay.io/pypa/manylinux2014_x86_64 (glibc 2.17, also used as fallback for < 2.17)
// - 2.28 <= glibc < 2.34: quay.io/pypa/manylinux_2_28_x86_64 (glibc 2.28, AlmaLinux 8)
// - glibc >= 2.34:        quay.io/pypa/manylinux_2_34_x86_64 (glibc 2.34, AlmaLinux 9)
// For x86_64 musllinux:
// - 1.1 <= musl < 1.2:    quay.io/pypa/musllinux_1_1_x86_64 (musl 1.1, Alpine 3.12/3.16)
// - musl >= 1.2:          quay.io/pypa/musllinux_1_2_x86_64 (musl 1.2, Alpine 3.20)
func SelectBaseImage(platformTags string) (string, error) {
	tags, err := ParsePlatformTags(platformTags)
	if err != nil {
		return "", errors.Wrapf(err, "parsing platform tags %q", platformTags)
	}
	lowest, err := LowestLibcVersionTag(tags)
	if err != nil {
		return "", errors.Wrapf(err, "determining lowest libc version tag for %q", platformTags)
	}
	if lowest.Arch != "x86_64" {
		return "", errors.Errorf("unsupported architecture %q in platform tag %q", lowest.Arch, platformTags)
	}
	switch lowest.LibcImpl {
	case Musl:
		// PEP 656 defines musl 1.1 as the minimum musllinux version.
		if lowest.LibcVersion.Less(Version{Major: 1, Minor: 1}) {
			return "", errors.Errorf("unsupported musl version %s in platform tag %q", lowest.LibcVersion, platformTags)
		}
		if lowest.LibcVersion.Less(Version{Major: 1, Minor: 2}) {
			return ImageMusllinux1_1X86_64, nil
		}
		return ImageMusllinux1_2X86_64, nil
	case Glibc:
		// PEP 513 (manylinux1) defines glibc 2.5 as the minimum manylinux version.
		if lowest.LibcVersion.Less(Version{Major: 2, Minor: 5}) {
			return "", errors.Errorf("unsupported glibc version %s in platform tag %q", lowest.LibcVersion, platformTags)
		}
		if lowest.LibcVersion.Less(Version{Major: 2, Minor: 28}) {
			return ImageManylinux2014X86_64, nil
		}
		if lowest.LibcVersion.Less(Version{Major: 2, Minor: 34}) {
			return ImageManylinux2_28X86_64, nil
		}
		return ImageManylinux2_34X86_64, nil
	default:
		return "", errors.Errorf("unknown libc family %q in platform tag %q", lowest.LibcImpl, platformTags)
	}
}
