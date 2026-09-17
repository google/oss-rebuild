// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import "log"

const (
	ImageManylinux2014X86_64 = "quay.io/pypa/manylinux2014_x86_64"
	ImageManylinux2_28X86_64 = "quay.io/pypa/manylinux_2_28_x86_64"
	ImageManylinux2_34X86_64 = "quay.io/pypa/manylinux_2_34_x86_64"

	ImageMusllinux1_1X86_64 = "quay.io/pypa/musllinux_1_1_x86_64"
	ImageMusllinux1_2X86_64 = "quay.io/pypa/musllinux_1_2_x86_64"
)

// Select the appropriate PyPA container image for a given platform tag with support for PEP 425
// single and dot-compressed tag sets
//
// For x86_64 manylinux:
// - glibc <= 2.17: quay.io/pypa/manylinux2014_x86_64 (covers manylinux1, 2010, 2014, 2_17)
// - glibc <= 2.28: quay.io/pypa/manylinux_2_28_x86_64 (glibc 2.28, AlmaLinux 8)
// - glibc > 2.28:  quay.io/pypa/manylinux_2_34_x86_64 (glibc 2.34, AlmaLinux 9)
// For x86_64 musllinux:
// - musl <= 1.1: quay.io/pypa/musllinux_1_1_x86_64 (musl 1.1, Alpine 3.12/3.16)
// - musl > 1.1:  quay.io/pypa/musllinux_1_2_x86_64 (musl 1.2, Alpine 3.20)
func SelectBaseImage(platformTags string) string {
	tags, err := ParsePlatformTags(platformTags)
	if err != nil || len(tags) == 0 {
		log.Printf("Warning: platform tag %q does not match supported platform tags (%v); falling back to default image %s", platformTags, err, ImageManylinux2014X86_64)
		return ImageManylinux2014X86_64
	}
	lowest, err := LowestLibcVersionTag(tags)
	if err != nil {
		log.Printf("Warning: failed to determine lowest baseline tag for %q (%v); falling back to default image %s", platformTags, err, ImageManylinux2014X86_64)
		return ImageManylinux2014X86_64
	}

	var selectedImage string
	switch lowest.LibcImpl {
	case Musl:
		switch {
		case lowest.LibcVersion.Major == 1 && lowest.LibcVersion.Minor <= 1:
			selectedImage = ImageMusllinux1_1X86_64
		default:
			selectedImage = ImageMusllinux1_2X86_64
		}
	case Glibc:
		switch {
		// Legacy mapping from https://packaging.python.org/en/latest/specifications/platform-compatibility-tags/#manylinux
		case lowest.LibcVersion.Major < 2 || (lowest.LibcVersion.Major == 2 && lowest.LibcVersion.Minor <= 17):
			selectedImage = ImageManylinux2014X86_64
		case lowest.LibcVersion.Major == 2 && lowest.LibcVersion.Minor <= 28:
			selectedImage = ImageManylinux2_28X86_64
		default:
			selectedImage = ImageManylinux2_34X86_64
		}
	default:
		log.Printf("Warning: unknown libc family %q in platform tag %q; falling back to %s", lowest.LibcImpl, platformTags, ImageManylinux2014X86_64)
		selectedImage = ImageManylinux2014X86_64
	}
	if lowest.Arch != "x86_64" {
		log.Printf("Warning: platform tag %q architecture %q is not x86_64; falling back to x86_64 image %s", platformTags, lowest.Arch, selectedImage)
	}
	return selectedImage
}
