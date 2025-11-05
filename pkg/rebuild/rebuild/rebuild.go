// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/gitx"
)

// Rebuilder defines the operations used to rebuild an ecosystem's packages.
type Rebuilder interface {
	InferRepo(context.Context, Target, RegistryMux) (string, error)
	CloneRepo(context.Context, Target, string, *gitx.RepositoryOptions) (RepoConfig, error)
	InferStrategy(context.Context, Target, RegistryMux, *RepoConfig, Strategy) (Strategy, error)
	Rebuild(context.Context, Target, Instructions, billy.Filesystem) error
	Compare(context.Context, Target, Asset, Asset, AssetStore, Instructions) (error, error)
	NeedsPrivilege(Input) bool
	UsesTimewarp(Input) bool
	UpstreamURL(context.Context, Target, RegistryMux) (string, error)
}
