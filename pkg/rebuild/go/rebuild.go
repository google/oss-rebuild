// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package golang

import (
	"context"
	"fmt"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Rebuilder implements the rebuild.Rebuilder interface for Go.
type Rebuilder struct{}

// UsesTimewarp indicates that this rebuilder uses timewarp.
func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
}

// UpstreamURL returns the upstream URL for a given Go module.
func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// Assumes the package name is the module path.
	// See https://go.dev/ref/mod#proxy-protocol
	return fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.zip", t.Package, t.Version), nil
}

var _ rebuild.Rebuilder = Rebuilder{}
