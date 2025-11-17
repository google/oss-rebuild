// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"context"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

// We expect target.Packge to be in the form "<component>/<name>".
func ParseComponent(pkg string) (component, name string, err error) {
	component, name, found := strings.Cut(pkg, "/")
	if !found {
		return "", "", errors.Errorf("failed to parse debian component: %s", pkg)
	}
	return component, name, nil
}

func (r Rebuilder) UsesTimewarp(input rebuild.Input) bool {
	return false
}

func (r Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	_, name, err := ParseComponent(t.Package)
	if err != nil {
		return "", errors.Wrap(err, "parsing package name")
	}
	return mux.Debian.ArtifactURL(ctx, name, t.Artifact)
}
