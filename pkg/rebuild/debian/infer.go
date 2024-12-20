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

package debian

import (
	"context"
	"regexp"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/debian"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	"github.com/pkg/errors"
)

// InferRepo is not needed because debian uses source packages.
func (Rebuilder) InferRepo(_ context.Context, _ rebuild.Target, _ rebuild.RegistryMux) (string, error) {
	return "", nil
}

// CloneRepo is not needed because debian uses source packages.
func (Rebuilder) CloneRepo(_ context.Context, _ rebuild.Target, _ string, _ billy.Filesystem, _ storage.Storer) (rebuild.RepoConfig, error) {
	return rebuild.RepoConfig{}, nil
}

// Source packages are expected to end with .tar.gz in format 3.0 or .diff.gz in format 1.0:
// https://wiki.debian.org/Packaging/SourcePackage#The_definition_of_a_source_package
// In the wild, we've seen a few additional compression schemes used.
var origRegex = regexp.MustCompile(`\.orig\.tar\.(gz|xz|bz2)$`)
var debianRegex = regexp.MustCompile(`\.(debian\.tar|diff)\.(gz|xz|bz2)$`)
var nativeRegex = regexp.MustCompile(`\.tar\.(gz|xz|bz2)$`)

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	p := DebianPackage{}
	var dsc *debian.DSC
	p.DSC.URL, dsc, err = mux.Debian.DSC(ctx, component, name, t.Version)
	if err != nil {
		return nil, err
	}
	for stanza := range dsc.Stanzas {
		for field, values := range dsc.Stanzas[stanza].Fields {
			switch field {
			case "Files":
				for _, value := range values {
					elems := strings.Split(strings.TrimSpace(value), " ")
					if len(elems) != 3 {
						return nil, errors.Errorf("unexpected dsc File element: %s", value)
					}
					md5 := elems[0]
					f := elems[2]
					if origRegex.FindStringIndex(f) != nil {
						p.Orig.URL = debianreg.PoolURL(component, name, f)
						p.Orig.MD5 = md5
					} else if debianRegex.FindStringIndex(f) != nil {
						p.Debian.URL = debianreg.PoolURL(component, name, f)
						p.Debian.MD5 = md5
					} else if nativeRegex.FindStringIndex(f) != nil {
						if p.Native.URL != "" {
							return nil, errors.Errorf("multiple matches for native source: %s, %s", p.Native.URL, f)
						}
						p.Native.URL = debianreg.PoolURL(component, name, f)
						p.Native.MD5 = md5
					}
				}
			case "Build-Depends", "Build-Depends-Indep":
				deps := strings.Split(strings.TrimSpace(values[0]), ",")
				for i, dep := range deps {
					dep = strings.TrimSpace(dep)
					if strings.Contains(dep, " ") {
						deps[i] = strings.TrimSpace(strings.Split(dep, " ")[0])
					}
				}
				p.Requirements = append(p.Requirements, deps...)
			}
		}
	}
	if (p.Orig.URL == "" || p.Debian.URL == "") && (p.Native.URL == "") {
		return nil, errors.Errorf("failed to find source files in the .dsc file: %s", p.DSC.URL)
	}
	return &p, nil
}
