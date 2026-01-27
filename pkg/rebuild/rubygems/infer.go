// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	gem, err := mux.RubyGems.Gem(ctx, t.Package)
	if err != nil {
		return "", err
	}

	// Priority order for finding repository:
	// 1. source_code_uri - explicit source code link
	// 2. homepage_uri - often points to GitHub
	// 3. bug_tracker_uri - sometimes contains repo info

	// Try source_code_uri first
	if gem.SourceCode != "" {
		if repo := uri.FindCommonRepo(gem.SourceCode); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
		// Even if not a known host, use it if it looks like a repo
		return uri.CanonicalizeRepoURI(gem.SourceCode)
	}

	// Try homepage
	if gem.Homepage != "" {
		if repo := uri.FindCommonRepo(gem.Homepage); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
	}

	// Try bug tracker
	if gem.BugTracker != "" {
		if repo := uri.FindCommonRepo(gem.BugTracker); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
	}

	return "", errors.New("no git repo found")
}

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, ropt *gitx.RepositoryOptions) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, ropt.Storer, ropt.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
		return r, nil
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	cfg := &GemBuild{}

	var ref, dir string
	lh, ok := hint.(*rebuild.LocationHint)
	if hint != nil && !ok {
		return nil, errors.Errorf("unsupported hint type: %T", hint)
	}

	if lh != nil && lh.Ref != "" {
		ref = lh.Ref
		if lh.Dir != "" {
			dir = lh.Dir
		} else {
			dir = rcfg.Dir
		}
	} else {
		// Try to find a matching git tag for this version
		tagHeuristic, err := rebuild.FindTagMatch(t.Package, t.Version, rcfg.Repository)
		if err != nil {
			return cfg, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
		}
		if tagHeuristic == "" {
			return cfg, errors.New("no git ref found")
		}
		ref = tagHeuristic
		dir = rcfg.Dir
	}

	return &GemBuild{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Dir:  dir,
			Ref:  ref,
		},
	}, nil
}
