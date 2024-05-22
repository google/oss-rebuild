// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package semver implements the Semantic Versioning 2.0.0 spec.
package semver

import (
	"cmp"
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
	if a == "" {
		return 1
	} else if b == "" {
		return -1
	}
	aas, ans := prereleaseKeys(a)
	bas, bns := prereleaseKeys(b)
	for i := 0; i < min(len(aas), len(bas)); i++ {
		if aas[i] != bas[i] {
			return strings.Compare(aas[i], bas[i])
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
	switch {
	case av.Major != bv.Major:
		return cmp.Compare(av.Major, bv.Major)
	case av.Minor != bv.Minor:
		return cmp.Compare(av.Minor, bv.Minor)
	case av.Patch != bv.Patch:
		return cmp.Compare(av.Patch, bv.Patch)
	case av.Prerelease != bv.Prerelease:
		return prereleaseCmp(av.Prerelease, bv.Prerelease)
	default:
		// Build metadata does not participate in ordering.
		return 0
	}
}
