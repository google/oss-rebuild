// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"errors"
	"log"
	"strings"

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

func warmCacheforArtifact(ctx context.Context, registry RegistryMux, t Target) {
	switch t.Ecosystem {
	case NPM:
		registry.NPM.Package(ctx, t.Package)
		registry.NPM.Version(ctx, t.Package, t.Version)
		registry.NPM.Artifact(ctx, t.Package, t.Version)
	case PyPI:
		registry.PyPI.Project(ctx, t.Package)
		registry.PyPI.Release(ctx, t.Package, t.Version)
		registry.PyPI.Artifact(ctx, t.Package, t.Version, t.Artifact)
	case CratesIO:
		registry.CratesIO.Crate(ctx, t.Package)
		registry.CratesIO.Version(ctx, t.Package, t.Version)
		registry.CratesIO.Artifact(ctx, t.Package, t.Version)
	case Maven:
		registry.Maven.PackageMetadata(ctx, t.Package)
		registry.Maven.PackageVersion(ctx, t.Package, t.Version)
	case Debian:
		component, name, found := strings.Cut(t.Package, "/")
		if !found {
			log.Printf("warming cache failed, expected component in debian package name %s", t.Package)
			return
		}
		registry.Debian.DSC(ctx, component, name, t.Version)
		registry.Debian.Artifact(ctx, component, name, t.Artifact)
	case RubyGems:
		registry.RubyGems.Gem(ctx, t.Package)
		registry.RubyGems.Versions(ctx, t.Package)
		registry.RubyGems.Artifact(ctx, t.Package, t.Version)
	}
}

func warmCacheForPackage(ctx context.Context, registry RegistryMux, t Target) {
	switch t.Ecosystem {
	case NPM:
		registry.NPM.Package(ctx, t.Package)
	case PyPI:
		registry.PyPI.Project(ctx, t.Package)
	case CratesIO:
		registry.CratesIO.Crate(ctx, t.Package)
	case Maven:
		registry.Maven.PackageMetadata(ctx, t.Package)
	case Debian:
		// There is no Debian resource shared across versions.
	case RubyGems:
		registry.RubyGems.Gem(ctx, t.Package)
	}
}
