// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// Rebuilder implements the Rebuilder interface for OCI registries.
type Rebuilder struct{}

var _ rebuild.Rebuilder = &Rebuilder{}

// InferRepo identifies the repository for an OCI target.
func (r *Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	return "", errors.New("not implemented")
}

// CloneRepo clones the repository for an OCI target.
func (r *Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repo string, opts *gitx.RepositoryOptions) (rebuild.RepoConfig, error) {
	return rebuild.RepoConfig{}, errors.New("not implemented")
}

// InferStrategy identifies the rebuild strategy for an OCI target.
func (r *Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, cfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	return nil, errors.New("not implemented")
}

// UsesTimewarp returns true if the rebuilder uses timewarp.
func (r *Rebuilder) UsesTimewarp(rebuild.Input) bool {
	return false
}

// Upstream returns the upstream artifact for an OCI target.
func (r *Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

// UpstreamURL returns the URL of the upstream artifact for an OCI target.
func (r *Rebuilder) UpstreamURL(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	return "", errors.New("not implemented")
}
