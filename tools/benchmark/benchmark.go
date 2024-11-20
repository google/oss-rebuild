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
