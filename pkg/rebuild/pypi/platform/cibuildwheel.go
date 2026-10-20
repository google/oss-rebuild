// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"strings"
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
	// Commit is the hash of the commit that the release tag points to.
	Commit string
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
// highest matching cibuildwheel release pins, along with that release's
// version.
//
// When specs are given (each an exact version like "2.16.2", a minor series
// like "2.16", or a release tag commit hash), only releases matching at least
// one spec are considered. When no specs are given, any release published at
// or before t is considered.
func (p CibuildwheelPinTable) ImageAt(repo string, t time.Time, specs ...string) (ref, version string, ok bool) {
	var best *CibuildwheelRelease
	for i, rel := range p {
		if _, pinned := rel.Images[repo]; !pinned || !rel.matches(t, specs) {
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

// matches reports whether r satisfies any spec in specs at time t, or whether r
// was published at or before t when specs is empty.
func (r CibuildwheelRelease) matches(t time.Time, specs []string) bool {
	if len(specs) == 0 {
		return !r.Published.After(t)
	}
	for _, spec := range specs {
		switch {
		case r.Version == spec, r.Commit == spec:
			return true
		case strings.Count(spec, ".") == 1 && strings.HasPrefix(r.Version, spec+"."):
			if !r.Published.After(t) {
				return true
			}
		}
	}
	return false
}

// reference returns the reference to the image in repo. The digest takes
// precedence over the tag, which is kept for readability.
func (i PinnedImage) reference(repo string) string {
	if i.Tag == "" {
		return repo + "@" + i.Digest
	}
	return repo + ":" + i.Tag + "@" + i.Digest
}
