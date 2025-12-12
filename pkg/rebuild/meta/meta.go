// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"

	"github.com/google/oss-rebuild/internal/httpx"
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	debianrb "github.com/google/oss-rebuild/pkg/rebuild/debian"
	mavenrb "github.com/google/oss-rebuild/pkg/rebuild/maven"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
)

func NewRegistryMux(c httpx.BasicClient) rebuild.RegistryMux {
	return rebuild.RegistryMux{
		Debian:   debianreg.HTTPRegistry{Client: c},
		CratesIO: cratesreg.HTTPRegistry{Client: c},
		NPM:      npmreg.HTTPRegistry{Client: c},
		PyPI:     pypireg.HTTPRegistry{Client: c},
		Maven:    mavenreg.HTTPRegistry{Client: c},
	}
}

var AllRebuilders = map[rebuild.Ecosystem]rebuild.Rebuilder{
	rebuild.NPM:      &npmrb.Rebuilder{},
	rebuild.PyPI:     &pypirb.Rebuilder{},
	rebuild.CratesIO: &cratesrb.Rebuilder{},
	rebuild.Debian:   &debianrb.Rebuilder{},
	rebuild.Maven:    &mavenrb.Rebuilder{},
}

func GuessArtifact(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	if t.Artifact != "" {
		return t.Artifact, nil
	}
	var guess string
	switch t.Ecosystem {
	case rebuild.NPM:
		guess = npmrb.ArtifactName(t)
	case rebuild.CratesIO:
		guess = cratesrb.ArtifactName(t)
	case rebuild.PyPI:
		release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
		if err != nil {
			return "", errors.Wrap(err, "fetching metadata failed")
		}
		a, err := pypirb.FindPureWheel(release.Artifacts)
		if err != nil {
			return "", errors.Wrap(err, "locating pure wheel failed")
		}
		guess = a.Filename
	case rebuild.Debian:
		return "", errors.New("debian requires explicit artifact")
	case rebuild.Maven:
		return "", errors.New("maven not implemented")
	default:
		return "", errors.New("unknown ecosystem")
	}
	if guess == "" {
		return "", errors.New("no artifact found")
	}
	return guess, nil
}
