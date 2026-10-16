// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"time"

	"github.com/google/oss-rebuild/internal/semver"
)

//go:generate go run gen_cibuildwheel_images.go

// CibuildwheelRelease records the build images that a cibuildwheel release pins.
type CibuildwheelRelease struct {
	// Version is the release version, such as "3.4.1".
	Version string
	// Published is when the release was uploaded to PyPI.
	Published time.Time
	// Images maps the supported image repositories that the release pins to
	// the pinned images.
	Images map[string]PinnedImage
}

// PinnedImage is a build image that a cibuildwheel release pins.
type PinnedImage struct {
	// Tag is the pinned tag. It is empty if the release pins by digest only.
	Tag string
	// Digest is the manifest digest of the image. It is empty if the tag had
	// expired before its digest was recorded.
	Digest string
}

// CibuildwheelPinTable lists cibuildwheel releases with the images they pin.
// CibuildwheelPins is the generated table. Lookups are methods on the table
// rather than functions over the generated one so that callers can be tested
// against a synthetic table.
type CibuildwheelPinTable []CibuildwheelRelease

// ImageAt returns the digest-pinned reference to the repo image that the
// highest cibuildwheel version published at or before t pins.
//
// Releases that do not pin repo are skipped, and the highest version that
// still pins it is used. ok is false if no release pins repo or if the digest of the
// selected pin is unknown.
func (p CibuildwheelPinTable) ImageAt(repo string, t time.Time) (ref, version string, ok bool) {
	var best *CibuildwheelRelease
	for i, rel := range p {
		if _, pinned := rel.Images[repo]; !pinned || rel.Published.After(t) {
			continue
		}
		if best == nil || semver.Cmp(rel.Version, best.Version) > 0 {
			best = &p[i]
		}
	}
	if best == nil {
		return "", "", false
	}
	// NOTE: A lower version is no fallback for an unknown digest because it
	// pins a different image.
	img := best.Images[repo]
	if img.Digest == "" {
		return "", "", false
	}
	return img.reference(repo), best.Version, true
}

// reference returns the reference to the image in repo. The digest takes
// precedence over the tag, which is kept for readability.
func (i PinnedImage) reference(repo string) string {
	if i.Tag == "" {
		return repo + "@" + i.Digest
	}
	return repo + ":" + i.Tag + "@" + i.Digest
}
