// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package meta

import (
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	debianrb "github.com/google/oss-rebuild/pkg/rebuild/debian"
	mavenrb "github.com/google/oss-rebuild/pkg/rebuild/maven"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var AllRebuilders = map[rebuild.Ecosystem]rebuild.Rebuilder{
	rebuild.NPM:      &npmrb.Rebuilder{},
	rebuild.PyPI:     &pypirb.Rebuilder{},
	rebuild.CratesIO: &cratesrb.Rebuilder{},
	rebuild.Debian:   &debianrb.Rebuilder{},
	rebuild.Maven:    &mavenrb.Rebuilder{},
}
