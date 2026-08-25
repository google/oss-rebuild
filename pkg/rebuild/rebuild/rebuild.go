// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"
	"io"

	"github.com/google/oss-rebuild/internal/gitx"
)

// RepoConfig describes the repo currently being used.
type RepoConfig struct {
	gitx.Repo // NOTE: by value so Repository access on a zero RepoConfig is nil and not panic
	URI       string
	Dir       string
	RefMap    map[string]string
}

// Rebuilder defines the operations used to rebuild an ecosystem's packages.
type Rebuilder interface {
	InferRepo(context.Context, Target, RegistryMux) (string, error)
	CloneRepo(context.Context, Target, string, *gitx.RepositoryOptions) (RepoConfig, error)
	InferStrategy(context.Context, Target, RegistryMux, *RepoConfig, Strategy) (Strategy, error)
	UsesTimewarp(Input) bool
	Upstream(context.Context, Target, RegistryMux) (io.ReadCloser, error)
	UpstreamURL(context.Context, Target, RegistryMux) (string, error)
}
