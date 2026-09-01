// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package versionx orders version strings across ecosystems, approximately.
//
// Strict semver 2.0.0 [semver] is compared per the spec. That covers npm and
// crates.io, whose registries require semver versions. Every other form is
// compared segment-wise: runs of digits and runs of letters compare left to
// right, numerically where both are digits, with digit runs ordering after
// letter runs and a version ordering after any proper prefix of itself.
// Checked against the ordering rules and worked examples of the remaining
// ecosystems' schemes, the segment-wise fallback agrees on:
//
//   - Final releases under every scheme: dotted numeric forms order
//     identically under PEP 440 [pep440], Gem::Version [gem], Maven [maven],
//     and Debian policy 5.6.12 [deb].
//   - Ordering within a pre-release stage, since numeric runs compare as
//     numbers: 1.0a2 < 1.0a12 (PEP 440), 1-foo2 < 1-foo10 (Maven).
//   - Suffixes that order *after* the release: PEP 440 .postN, Debian +deb
//     style additions, and Maven sp all order after the bare version.
//   - Digit runs after letter runs: Maven specifies 1.7 > 1.K, and the same
//     rule yields PEP 440's 1.0.post1 < 1.0.15.
//   - Maven's pre-release qualifiers among themselves: the specified order
//     alpha < beta < milestone < rc < snapshot is alphabetical, so it holds
//     with no qualifier table.
//
// It diverges on each scheme's pre-release marker. The fallback orders every
// suffixed form after its base, while each scheme defines a marker that
// orders before it:
//
//   - PEP 440 pre-releases: the spec orders suffixes ".devN, aN, bN, rcN,
//     <no suffix>, .postN", so 1.0rc1 < 1.0 there but 1.0rc1 > 1.0 here.
//   - Debian "~": it "sorts before anything, even the end of a part", so
//     1.0~rc1 < 1.0 there but after here.
//   - Maven qualifiers: alpha through snapshot precede the bare version.
//   - Gem::Version prereleases: a part containing letters marks the version
//     as a prerelease that sorts below its release, so 1.0.b1 < 1.0 there.
//   - semver "-" pre-releases, whenever a non-semver counterpart forces the
//     fallback path (hence the intransitivity note on ApproxCompare).
//   - Punctuation-encoded features: epoch markers (PEP 440 "N!", Debian
//     "N:") and Debian's upstream/revision "-" boundary are discarded with
//     all other non-alphanumerics and cannot affect the ordering.
//
// Spec references:
//
//	[semver] https://semver.org/spec/v2.0.0.html#spec-item-11
//	[pep440] https://peps.python.org/pep-0440/#summary-of-permitted-suffixes-and-relative-ordering
//	[gem]    https://docs.ruby-lang.org/en/3.3/Gem/Version.html
//	[maven]  https://maven.apache.org/pom.html#version-order-specification
//	[deb]    https://www.debian.org/doc/debian-policy/ch-controlfields.html#version
package versionx

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/oss-rebuild/internal/semver"
)

// ApproxCompare orders two version strings. Strict semver versions are
// compared per the spec. Otherwise the versions are compared segment-wise,
// numerically where possible. The ordering is approximate for exotic version
// schemes and fit only for uses where an approximate order beats none (e.g.
// picking the nearest definition, or a package's probable newest version).
// NOTE: The ordering is intransitive when candidates mix semver and
// non-semver forms: semver prerelease ordering (1.0.0-rc1 < 1.0.0) can
// contradict compareLoose's segment-wise ordering of the same strings
// (1.0.0-rc1 > 1.0.0.post1 > 1.0.0), so sorts over mixed candidate sets may
// pick a non-nearest element.
func ApproxCompare(a, b string) int {
	av, aerr := semver.New(a)
	bv, berr := semver.New(b)
	if aerr == nil && berr == nil {
		return av.Compare(bv)
	}
	return compareLoose(a, b)
}

var versionSegmentRE = regexp.MustCompile(`\d+|[a-zA-Z]+`)

func compareLoose(a, b string) int {
	as := versionSegmentRE.FindAllString(a, -1)
	bs := versionSegmentRE.FindAllString(b, -1)
	for i := range min(len(as), len(bs)) {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return cmp.Compare(an, bn)
			}
		case aerr == nil:
			// Numeric segments order after alphabetic ones (e.g. 1.0.1 > 1.0.rc).
			return 1
		case berr == nil:
			return -1
		default:
			if as[i] != bs[i] {
				return strings.Compare(as[i], bs[i])
			}
		}
	}
	return cmp.Compare(len(as), len(bs))
}
