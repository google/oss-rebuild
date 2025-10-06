// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package meta

import (
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
