// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"context"
	"log"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	registry "github.com/google/oss-rebuild/pkg/registry/rubygems"
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

	// Infer Ruby and RubyGems versions from the upstream gem's metadata.
	var rubyVersion, rubygemsVersion string
	if spec, err := parseUpstreamGemSpec(ctx, t, mux); err != nil {
		log.Printf("warning: failed to extract gem spec from %s@%s: %v", t.Package, t.Version, err)
	} else if rgv := spec.RubygemsVersion; rgv == "" {
		log.Printf("warning: rubygems_version not found in gem spec for %s@%s", t.Package, t.Version)
	} else {
		rv, exact, err := rubyVersionForRubygems(rgv)
		if err != nil {
			log.Printf("warning: failed to map rubygems_version %s to ruby: %v", rgv, err)
		} else {
			rubyVersion = rv
			if !exact && rgv == cleanRubygemsVersion(rgv) {
				// The bundled RubyGems version doesn't match; install it explicitly.
				// Skip dev/pre versions as they aren't available via gem update --system.
				rubygemsVersion = rgv
			}
		}
	}
	if rubyVersion == "" {
		// Fallback: recent stable Ruby available from ruby-builder.
		rubyVersion = "3.3.11"
	}

	return &GemBuild{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Dir:  dir,
			Ref:  ref,
		},
		RubyVersion:     rubyVersion,
		RubygemsVersion: rubygemsVersion,
	}, nil
}

// parseUpstreamGemSpec downloads the upstream .gem file and parses its
// embedded gem specification.
func parseUpstreamGemSpec(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (*registry.GemSpec, error) {
	rc, err := mux.RubyGems.Artifact(ctx, t.Package, t.Version)
	if err != nil {
		return nil, errors.Wrap(err, "downloading gem")
	}
	defer rc.Close()
	return registry.ParseGemSpec(rc)
}
