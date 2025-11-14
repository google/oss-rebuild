// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rebuild provides functionality to rebuild packages.
package rebuild

import (
	"context"
	"io"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
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

// Summarize constructs ContentSummary objects for the upstream and rebuilt artifacts.
func Summarize(ctx context.Context, t Target, rb, up Asset, assets AssetStore) (csRB, csUP *archive.ContentSummary, err error) {
	{ // Summarize rebuild
		r, err := assets.Reader(ctx, rb)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "[INTERNAL] Failed to find rebuilt artifact")
		}
		defer r.Close()
		csRB, err = archive.NewContentSummary(r, t.ArchiveType())
		if err != nil {
			return nil, nil, errors.Wrapf(err, "[INTERNAL] Failed to calculate rebuild content summary")
		}
	}
	{ // Summarize upstream
		r, err := assets.Reader(ctx, up)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "[INTERNAL] Failed to find upstream artifact")
		}
		defer r.Close()
		csUP, err = archive.NewContentSummary(r, t.ArchiveType())
		if err != nil {
			return nil, nil, errors.Wrapf(err, "[INTERNAL] Failed to calculate upstream content summary")
		}
	}
	return csRB, csUP, nil
}
