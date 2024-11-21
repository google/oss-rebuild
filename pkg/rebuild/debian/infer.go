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
	"strings"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	debreg "github.com/google/oss-rebuild/pkg/registry/debian"
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

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	component, name, err := ParseComponent(t.Package)
	if err != nil {
		return nil, err
	}
	dscURI, dsc, err := mux.Debian.DSC(ctx, component, name, t.Version)
	if err != nil {
		return nil, err
	}
	var orig, debianTar, native FileWithChecksum
	var dependencies []string
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
					if strings.HasSuffix(f, ".orig.tar.xz") {
						orig.URL = debreg.PoolURL(component, name, f)
						orig.MD5 = md5
						continue
					} else if strings.HasSuffix(f, ".debian.tar.xz") {
						debianTar.URL = debreg.PoolURL(component, name, f)
						debianTar.MD5 = md5
						continue
					} else if strings.HasSuffix(f, ".tar.xz") {
						native.URL = debreg.PoolURL(component, name, f)
						native.MD5 = md5
						continue
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
				dependencies = append(dependencies, deps...)
			}
		}
	}
	if (orig.URL == "" || debianTar.URL == "") && (native.URL == "") {
		return nil, errors.New("Failed to find source files in the .dsc file")
	}
	return &DebianPackage{
		DSC: FileWithChecksum{
			URL: dscURI,
		},
		Orig:         orig,
		Debian:       debianTar,
		Native:       native,
		Requirements: dependencies,
	}, nil
}
