// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rebuild provides functionality to rebuild packages.
package rebuild

import (
	"context"
	"io"
	"strings"

	"github.com/pkg/errors"
)

func UpstreamArtifactReader(ctx context.Context, t Target, mux RegistryMux) (io.ReadCloser, error) {
	// TODO: Make this configurable from within each ecosystem.
	switch t.Ecosystem {
	case NPM:
		return mux.NPM.Artifact(ctx, t.Package, t.Version)
	case PyPI:
		return mux.PyPI.Artifact(ctx, t.Package, t.Version, t.Artifact)
	case CratesIO:
		return mux.CratesIO.Artifact(ctx, t.Package, t.Version)
	case Debian:
		component, name, found := strings.Cut(t.Package, "/")
		if !found {
			return nil, errors.Errorf("failed to parse debian component: %s", t.Package)
		}
		return mux.Debian.Artifact(ctx, component, name, t.Artifact)
	case Maven:
		return mux.Maven.Artifact(ctx, t.Package, t.Version, t.Artifact)
	default:
		return nil, errors.New("unsupported ecosystem")
	}
}
