// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"context"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/gitx"
)

// RepoConfig describes the repo currently being used.
type RepoConfig struct {
	*git.Repository
	URI    string
	Dir    string
	RefMap map[string]string
}

// Rebuilder defines the operations used to rebuild an ecosystem's packages.
type Rebuilder interface {
	InferRepo(context.Context, Target, RegistryMux) (string, error)
	CloneRepo(context.Context, Target, string, *gitx.RepositoryOptions) (RepoConfig, error)
	InferStrategy(context.Context, Target, RegistryMux, *RepoConfig, Strategy) (Strategy, error)
	Rebuild(context.Context, Target, Instructions, billy.Filesystem) error
	Compare(context.Context, Target, Asset, Asset, AssetStore, Instructions) (error, error)
	UsesTimewarp(Input) bool
	UpstreamURL(context.Context, Target, RegistryMux) (string, error)
}
