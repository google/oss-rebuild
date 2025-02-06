// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package semver implements the Semantic Versioning 2.0.0 spec.
package semver

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type Semver struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
	Build      string
}

// Compare evaluates how this semver identifier is ordered with respect to "other"
func (s Semver) Compare(other Semver) int {
	// NOTE: Build metadata does not participate in ordering
	return cmp.Or(
		cmp.Compare(s.Major, other.Major),
		cmp.Compare(s.Minor, other.Minor),
		cmp.Compare(s.Patch, other.Patch),
		prereleaseCmp(s.Prerelease, other.Prerelease),
	)
}

func (s Semver) String() string {
	str := fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
	if s.Prerelease != "" {
		str += "-" + s.Prerelease
	}
	if s.Build != "" {
		str += "+" + s.Build
	}
	return str
}

// Adapted from: https://semver.org/spec/v2.0.0#is-there-a-suggested-regular-expression-regex-to-check-a-semver-string
var semverRE = regexp.MustCompile(`^v?(?P<Major>0|[1-9]\d*)\.(?P<Minor>0|[1-9]\d*)\.(?P<Patch>0|[1-9]\d*)(?:-(?P<Prerelease>(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+(?P<Build>[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

func New(s string) (Semver, error) {
	if !semverRE.MatchString(s) {
		return Semver{}, errors.Errorf("Invalid semver")
	}
	matches := semverRE.FindStringSubmatch(s)
	major, _ := strconv.Atoi(matches[semverRE.SubexpIndex("Major")])
	minor, _ := strconv.Atoi(matches[semverRE.SubexpIndex("Minor")])
	patch, _ := strconv.Atoi(matches[semverRE.SubexpIndex("Patch")])
	return Semver{
		major,
		minor,
		patch,
		matches[semverRE.SubexpIndex("Prerelease")],
		matches[semverRE.SubexpIndex("Build")],
	}, nil
}

var numericRE = regexp.MustCompile(`\d+`)

func prereleaseKey(p string) (alpha string, numeric int) {
	alpha = p
	if match := numericRE.FindAllStringIndex(p, -1); match != nil {
		last := match[len(match)-1]
		numeric, _ = strconv.Atoi(p[last[0]:last[1]])
		alpha = p[:last[0]]
	}
	return
}

func prereleaseKeys(p string) (alphas []string, numerics []int) {
	for _, part := range strings.Split(p, ".") {
		a, n := prereleaseKey(part)
		alphas = append(alphas, a)
		numerics = append(numerics, n)
	}
	return
}

func prereleaseCmp(a, b string) int {
	// A version lacking a prerelease value is always 'greater' than a version
	// with one. Shortcut the comparison logic to apply this rule, if applicable.
	if a == "" || b == "" {
		return -strings.Compare(a, b)
	}
	aas, ans := prereleaseKeys(a)
	bas, bns := prereleaseKeys(b)
	for i := range min(len(aas), len(bas)) {
		if aas[i] != bas[i] {
			return cmp.Compare(aas[i], bas[i])
		}
		if ans[i] != bns[i] {
			return cmp.Compare(ans[i], bns[i])
		}
	}
	return cmp.Compare(len(aas), len(bas))
}

func Cmp(a, b string) int {
	av, err := New(a)
	if err != nil {
		return -1
	}
	bv, err := New(b)
	if err != nil {
		return 1
	}
	return av.Compare(bv)
}
