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

// Package rebuild provides functionality to rebuild packages.
package rebuild

import (
	"context"
	"io"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

func artifactReader(ctx context.Context, t Target, mux RegistryMux) (io.ReadCloser, error) {
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
	default:
		return nil, errors.New("unsupported ecosystem")
	}
}

// Stabilize the upstream and rebuilt artifacts.
func Stabilize(ctx context.Context, t Target, mux RegistryMux, rbPath string, fs billy.Filesystem, assets AssetStore) (rb, up Asset, err error) {
	{ // Stabilize rebuild
		rb = Asset{Type: DebugRebuildAsset, Target: t}
		w, err := assets.Writer(ctx, rb)
		if err != nil {
			return rb, up, errors.Errorf("[INTERNAL] failed to store asset %v", rb)
		}
		defer w.Close()
		f, err := fs.Open(rbPath)
		if err != nil {
			return rb, up, errors.Wrapf(err, "[INTERNAL] Failed to find rebuilt artifact")
		}
		defer f.Close()
		if err := archive.Stabilize(w, f, t.ArchiveType()); err != nil {
			return rb, up, errors.Wrapf(err, "[INTERNAL] Stabilize rebuild failed")
		}
	}
	{ // Stabilize upstream
		up = Asset{Type: DebugUpstreamAsset, Target: t}
		w, err := assets.Writer(ctx, up)
		if err != nil {
			return rb, up, errors.Errorf("[INTERNAL] failed to store asset %v", up)
		}
		defer w.Close()
		r, err := artifactReader(ctx, t, mux)
		if err != nil {
			return rb, up, errors.Wrapf(err, "[INTERNAL] Failed to fetch upstream artifact")
		}
		defer r.Close()
		if err := archive.Stabilize(w, r, t.ArchiveType()); err != nil {
			return rb, up, errors.Wrapf(err, "[INTERNAL] Stabilize upstream failed")
		}
	}
	return rb, up, nil
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
