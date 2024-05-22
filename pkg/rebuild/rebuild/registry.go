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

package rebuild

import (
	"errors"

	cacheinternal "github.com/google/oss-rebuild/internal/cache"
	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/npm"
	"github.com/google/oss-rebuild/pkg/registry/pypi"
)

// RegistryMux offers a unified accessor for package registries.
type RegistryMux struct {
	NPM      npm.Registry
	PyPI     pypi.Registry
	CratesIO cratesio.Registry
}

// RegistryMuxWithCache returns a new RegistryMux with the provided cache wrapping each registry.
func RegistryMuxWithCache(registry RegistryMux, c cacheinternal.Cache) (RegistryMux, error) {
	var newmux RegistryMux
	if httpreg, ok := registry.NPM.(npm.HTTPRegistry); ok {
		newmux.NPM = npm.HTTPRegistry{Client: httpinternal.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown npm registry type")
	}
	if httpreg, ok := registry.PyPI.(pypi.HTTPRegistry); ok {
		newmux.PyPI = pypi.HTTPRegistry{Client: httpinternal.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown PyPI registry type")
	}
	if httpreg, ok := registry.CratesIO.(cratesio.HTTPRegistry); ok {
		newmux.CratesIO = cratesio.HTTPRegistry{Client: httpinternal.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown crates.io registry type")
	}
	return newmux, nil
}

func warmCacheforArtifact(registry RegistryMux, t Target) {
	switch t.Ecosystem {
	case NPM:
		registry.NPM.Package(t.Package)
		registry.NPM.Version(t.Package, t.Version)
		registry.NPM.Artifact(t.Package, t.Version)
	case PyPI:
		registry.PyPI.Project(t.Package)
		registry.PyPI.Release(t.Package, t.Version)
		registry.PyPI.Artifact(t.Package, t.Version, t.Artifact)
	case CratesIO:
		registry.CratesIO.Crate(t.Package)
		registry.CratesIO.Version(t.Package, t.Version)
		registry.CratesIO.Artifact(t.Package, t.Version)
	}
}

func warmCacheForPackage(registry RegistryMux, t Target) {
	switch t.Ecosystem {
	case NPM:
		registry.NPM.Package(t.Package)
	case PyPI:
		registry.PyPI.Project(t.Package)
	case CratesIO:
		registry.CratesIO.Crate(t.Package)
	}
}
