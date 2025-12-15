// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package golang

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// InferRepo infers the repository URL for a given Go module.
func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	// For Go, the package name often contains the repository URL.
	// TODO: maybe use the canonical repo func
	if strings.HasPrefix(t.Package, "github.com/") || strings.HasPrefix(t.Package, "golang.org/") {
		parts := strings.Split(t.Package, "/")
		if len(parts) >= 3 {
			repoPath := strings.Join(parts[0:3], "/")
			return fmt.Sprintf("https://%s", repoPath), nil
		}
	}
	return "", errors.New("could not infer repository for go module")
}

// CloneRepo clones the repository for a given Go module.
func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, ropt *gitx.RepositoryOptions) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, ropt.Storer, ropt.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
		return r, nil
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
}

// InferStrategy infers the build strategy for a given Go module.
func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	return &GoBuild{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Ref:  t.Version,
		},
	}, nil
}
