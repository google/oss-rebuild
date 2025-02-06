// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package benchmark provides interfaces related to rebuild benchmarks.
package benchmark

import (
	"hash"
	"slices"
	"strings"
	"time"
)

// PackageSet is a grouping of packages to evaluate rebuilds.
type PackageSet struct {
	Metadata
	Packages []Package
}

// Hash canonicalizes and returns a hash on its Packages using "h".
func (ps *PackageSet) Hash(h hash.Hash) []byte {
	var ids []string
	for _, p := range ps.Packages {
		for _, v := range p.Versions {
			ids = append(ids, strings.Join([]string{p.Ecosystem, p.Name, v}, "|"))
		}
	}
	slices.Sort(ids)
	h.Write([]byte(strings.Join(ids, "|")))
	return h.Sum(nil)
}

// Metadata describes characteristics of a PackageSet.
type Metadata struct {
	Count   int
	Updated time.Time
}

// Package corresponds to one or more versions of a package to rebuild.
//
// * Only the versions provided will be rebuilt.
// * All supported artifacts will be built for each provided version.
//
// TODO: Possible extension of this form would include specific artifacts:
//
//	 {
//	   ...,
//		  "artifacts": {"1.2.0": [...]},
//	 }
type Package struct {
	Ecosystem string
	Name      string
	Versions  []string
	Artifacts []string
}
