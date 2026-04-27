// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"errors"

	cacheinternal "github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/google/oss-rebuild/pkg/registry/debian"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/google/oss-rebuild/pkg/registry/npm"
	"github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/pkg/registry/rubygems"
)

// RegistryMux offers a unified accessor for package registries.
type RegistryMux struct {
	NPM      npm.Registry
	PyPI     pypi.Registry
	CratesIO cratesio.Registry
	Maven    maven.Registry
	Debian   debian.Registry
	RubyGems rubygems.Registry
}

// RegistryMuxWithCache returns a new RegistryMux with the provided cache wrapping each registry.
func RegistryMuxWithCache(registry RegistryMux, c cacheinternal.Cache) (RegistryMux, error) {
	var newmux RegistryMux
	if httpreg, ok := registry.NPM.(npm.HTTPRegistry); ok {
		newmux.NPM = npm.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown npm registry type")
	}
	if httpreg, ok := registry.PyPI.(pypi.HTTPRegistry); ok {
		newmux.PyPI = pypi.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown PyPI registry type")
	}
	if httpreg, ok := registry.CratesIO.(cratesio.HTTPRegistry); ok {
		newmux.CratesIO = cratesio.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown crates.io registry type")
	}
	if httpreg, ok := registry.Maven.(maven.HTTPRegistry); ok {
		newmux.Maven = maven.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown Maven Central registry type")
	}
	if httpreg, ok := registry.Debian.(debian.HTTPRegistry); ok {
		newmux.Debian = debian.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown debian registry type")
	}
	if httpreg, ok := registry.RubyGems.(rubygems.HTTPRegistry); ok {
		newmux.RubyGems = rubygems.HTTPRegistry{Client: httpx.NewCachedClient(httpreg.Client, c)}
	} else {
		return newmux, errors.New("unknown rubygems registry type")
	}
	return newmux, nil
}
