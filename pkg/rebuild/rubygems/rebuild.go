// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"fmt"
	"io"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Rebuilder implements the rebuild.Rebuilder interface for RubyGems.
type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

// UsesTimewarp returns whether this ecosystem uses time-based registry lookups.
func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
}

// Upstream returns a reader for the upstream artifact.
func (r Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	return mux.RubyGems.Artifact(ctx, t.Package, t.Version)
}

// UpstreamURL returns the URL for the upstream artifact.
func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// RubyGems URLs follow a predictable format
	return fmt.Sprintf("https://rubygems.org/gems/%s-%s.gem", t.Package, t.Version), nil
}

// ArtifactName returns the expected artifact name for a target.
func ArtifactName(t rebuild.Target) string {
	return fmt.Sprintf("%s-%s.gem", t.Package, t.Version)
}
