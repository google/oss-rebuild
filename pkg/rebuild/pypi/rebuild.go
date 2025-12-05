// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

var (
	verdictDSStore         = errors.New(".DS_STORE file(s) found in upstream but not rebuild")
	verdictLineEndings     = errors.New("Excess CRLF line endings found in upstream")
	verdictMismatchedFiles = errors.New("mismatched file(s) in upstream and rebuild")
	verdictUpstreamOnly    = errors.New("file(s) found in upstream but not rebuild")
	verdictRebuildOnly     = errors.New("file(s) found in rebuild but not upstream")
	verdictWheelDiff       = errors.New("wheel metadata mismatch")
	verdictContentDiff     = errors.New("content differences found")
)

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return true
}

func (r Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	return mux.PyPI.Artifact(ctx, t.Package, t.Version, t.Artifact)
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching project failed")
	}
	for _, a := range release.Artifacts {
		if a.Filename == t.Artifact {
			return a.URL, nil
		}
	}
	return "", errors.New("artifact not found")
}
