// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// ArtifactName is the primary jar of a "group:artifact" package version.
func ArtifactName(t rebuild.Target) string {
	_, a, found := strings.Cut(t.Package, ":")
	if !found {
		return ""
	}
	return fmt.Sprintf("%s-%s.jar", a, t.Version)
}

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
}

func (r Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	return mux.Maven.Artifact(ctx, t.Package, t.Version, t.Artifact)
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// Assuming the primary artifact is a .jar file.
	return mux.Maven.ReleaseURL(ctx, t.Package, t.Version, ".jar")
}
