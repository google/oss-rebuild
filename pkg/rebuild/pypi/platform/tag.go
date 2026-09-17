// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// LibcImpl denotes the kind of C standard library.
type LibcImpl string

const (
	Glibc LibcImpl = "glibc"
	Musl  LibcImpl = "musl"
)

// Version represents a C library major.minor version.
type Version struct {
	Major int `json:"major" yaml:"major"`
	Minor int `json:"minor" yaml:"minor"`
}

// Less returns true if v is a lower version than other.
func (v Version) Less(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	return v.Minor < other.Minor
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Tag represents a parsed platform tag (manylinux or musllinux).
type Tag struct {
	Raw         string   `json:"raw" yaml:"raw"`
	LibcImpl    LibcImpl `json:"libc" yaml:"libc"`
	LibcVersion Version  `json:"version" yaml:"version"`
	Arch        string   `json:"arch" yaml:"arch"`
}

var (
	// Legacy PEP 513, 571, 599 tag regexes:
	// manylinux1_x86_64, manylinux2010_x86_64, manylinux2014_x86_64
	manylinuxLegacyTagPat = regexp.MustCompile(`^manylinux(1|2010|2014)_([a-zA-Z0-9_]+)$`)

	// PEP 600 perpetual tag regex:
	// manylinux_${GLIBCMAJOR}_${GLIBCMINOR}_${ARCH}
	manylinuxTagPat = regexp.MustCompile(`^manylinux_(\d+)_(\d+)_([a-zA-Z0-9_]+)$`)

	// PEP 656 musllinux tag regex:
	// musllinux_${MUSLMAJOR}_${MUSLMINOR}_${ARCH}
	musllinuxTagPat = regexp.MustCompile(`^musllinux_(\d+)_(\d+)_([a-zA-Z0-9_]+)$`)
)

// parseTag parses a single platform tag
//
// For now we only support linux platforms.
// Attempting to parse platform tags for other platforms results in an error.
func parseTag(raw string) (Tag, error) {
	if matches := manylinuxLegacyTagPat.FindStringSubmatch(raw); matches != nil {
		alias := matches[1]
		arch := matches[2]
		var version Version
		switch alias {
		// This mapping is taken from https://packaging.python.org/en/latest/specifications/platform-compatibility-tags/#manylinux
		case "1":
			version = Version{Major: 2, Minor: 5}
		case "2010":
			version = Version{Major: 2, Minor: 12}
		case "2014":
			version = Version{Major: 2, Minor: 17}
		}
		return Tag{
			Raw:         raw,
			LibcImpl:    Glibc,
			Arch:        arch,
			LibcVersion: version,
		}, nil
	}

	if matches := manylinuxTagPat.FindStringSubmatch(raw); matches != nil {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return Tag{}, errors.Wrapf(err, "invalid glibc major version in %s", raw)
		}
		minor, err := strconv.Atoi(matches[2])
		if err != nil {
			return Tag{}, errors.Wrapf(err, "invalid glibc minor version in %s", raw)
		}
		arch := matches[3]
		return Tag{
			Raw:         raw,
			LibcImpl:    Glibc,
			Arch:        arch,
			LibcVersion: Version{Major: major, Minor: minor},
		}, nil
	}

	if matches := musllinuxTagPat.FindStringSubmatch(raw); matches != nil {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return Tag{}, errors.Wrapf(err, "invalid musl major version in %s", raw)
		}
		minor, err := strconv.Atoi(matches[2])
		if err != nil {
			return Tag{}, errors.Wrapf(err, "invalid musl minor version in %s", raw)
		}
		arch := matches[3]
		return Tag{
			Raw:         raw,
			LibcImpl:    Musl,
			Arch:        arch,
			LibcVersion: Version{Major: major, Minor: minor},
		}, nil
	}

	return Tag{}, errors.Errorf("Unsupported platform tag: %s", raw)
}

// ParsePlatformTags parses PEP 425 wheel platform tags.
// It supports both, single tags and dot-delimited compressed tags.
func ParsePlatformTags(platformTags string) ([]Tag, error) {
	if platformTags == "" {
		return nil, errors.New("empty platform tag")
	}
	parts := strings.Split(platformTags, ".")
	tags := make([]Tag, 0, len(parts))
	for _, p := range parts {
		tag, err := parseTag(p)
		if err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// LowestLibcVersionTag returns the tag with the lowest libc version from a list of tags.
func LowestLibcVersionTag(tags []Tag) (Tag, error) {
	if len(tags) == 0 {
		return Tag{}, errors.New("no tags provided")
	}
	return slices.MinFunc(tags, func(a, b Tag) int {
		if a.LibcVersion.Less(b.LibcVersion) {
			return -1
		}
		if b.LibcVersion.Less(a.LibcVersion) {
			return 1
		}
		return 0
	}), nil
}

// LowestLibcTagString parses raw platform tags (which means potentially multiple compressed tags)
// from a wheel and returns the individual tag with the lowest associated libc version as a string.
// Returns the input tags if parsing fails.
func LowestLibcTagString(platformTags string) string {
	tags, err := ParsePlatformTags(platformTags)
	if err != nil || len(tags) == 0 {
		return platformTags
	}
	lowest, err := LowestLibcVersionTag(tags)
	if err != nil {
		return platformTags
	}
	return lowest.Raw
}
