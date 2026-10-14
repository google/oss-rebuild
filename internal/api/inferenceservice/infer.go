// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/api/cratesregistryservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/gitcache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/registry/cratesio"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type cratesIOVersionCache struct {
	cratesio.Registry
	name    string
	version string
	value   *cratesio.CrateVersion
}

func (c cratesIOVersionCache) Version(ctx context.Context, name, version string) (*cratesio.CrateVersion, error) {
	if name == c.name && version == c.version {
		return c.value, nil
	}
	return c.Registry.Version(ctx, name, version)
}

func withCratesIOFreshness(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (context.Context, rebuild.RegistryMux, error) {
	if t.Ecosystem != rebuild.CratesIO {
		return ctx, mux, nil
	}
	cache, ok := ctx.Value(rebuild.RepoCacheClientID).(*gitcache.Client)
	if !ok || cache == nil {
		return ctx, mux, nil
	}
	vmeta, err := mux.CratesIO.Version(ctx, t.Package, t.Version)
	if err != nil {
		return ctx, mux, errors.Wrap(err, "getting crates.io version freshness")
	}
	if vmeta == nil {
		return ctx, mux, errors.New("getting crates.io version freshness: empty version metadata")
	}
	cacheCopy := *cache
	cacheCopy.DefaultFreshness = vmeta.Created
	ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, &cacheCopy)
	mux.CratesIO = cratesIOVersionCache{Registry: mux.CratesIO, name: t.Package, version: t.Version, value: vmeta}
	return ctx, mux, nil
}

func doInfer(ctx context.Context, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux, hint rebuild.Strategy, ropt *gitx.RepositoryOptions) (rebuild.Strategy, rebuild.RepoConfig, error) {
	var err error
	ctx, mux, err = withCratesIOFreshness(ctx, t, mux)
	if err != nil {
		return nil, rebuild.RepoConfig{}, err
	}
	var repo string
	if lh, ok := hint.(*rebuild.LocationHint); ok && lh != nil {
		repo, err = uri.CanonicalizeRepoURI(lh.Location.Repo)
		if err != nil {
			return nil, rebuild.RepoConfig{}, errors.Wrap(err, "canonicalizing repo hint")
		}
	} else {
		repo, err = rebuilder.InferRepo(ctx, t, mux)
		if err != nil {
			return nil, rebuild.RepoConfig{}, err
		}
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, ropt)
	if err != nil {
		return nil, rebuild.RepoConfig{}, err
	}
	strategy, err := rebuilder.InferStrategy(ctx, t, mux, &rcfg, hint)
	if err != nil {
		return nil, rcfg, err
	}
	return strategy, rcfg, nil
}

type InferDeps struct {
	HTTPClient         httpx.BasicClient
	GitCache           *gitcache.Client
	RepoOptF           func() *gitx.RepositoryOptions
	CratesRegistryStub api.StubFn[cratesregistryservice.FindRegistryCommitRequest, cratesregistryservice.FindRegistryCommitResponse]
	RepoMetrics        db.RepoMetrics // optional. when nil, repos are not measured on clone
}

// recordRepoMetrics measures rcfg and upserts its repo_metrics record keyed
// by canonical repo URI.
func recordRepoMetrics(ctx context.Context, store db.RepoMetrics, rcfg rebuild.RepoConfig) error {
	canonical, err := uri.CanonicalizeRepoURI(rcfg.URI)
	if err != nil {
		return errors.Wrapf(err, "canonicalizing %q", rcfg.URI)
	}
	if rcfg.Repository == nil {
		return errors.New("nil repository")
	}
	ref, err := rcfg.Head()
	if err != nil {
		return errors.Wrap(err, "resolving head")
	}
	bytes, err := gitx.ObjectStoreSize(rcfg.Storer)
	if err != nil {
		return errors.Wrap(err, "sizing object store")
	}
	commits, err := gitx.CommitCount(rcfg.Storer)
	if err != nil {
		return errors.Wrap(err, "counting commits")
	}
	m := schema.RepoMetrics{
		URI:        canonical,
		Bytes:      bytes,
		Commits:    commits,
		Head:       ref.Hash().String(),
		MeasuredAt: rcfg.FetchedAt,
		Updated:    time.Now().UTC(),
	}
	return errors.Wrap(store.Upsert(ctx, m), "upserting")
}

func Infer(ctx context.Context, req schema.InferenceRequest, deps *InferDeps) (*schema.StrategyOneOf, error) {
	if req.LocationHint() != nil && req.LocationHint().Ref == "" && req.LocationHint().Dir != "" {
		return nil, api.AsStatus(codes.Unimplemented, errors.New("location hint dir without ref not implemented"))
	}
	if req.LocationHint() != nil && req.LocationHint().Repo == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("location hint without repo is not supported"))
	}
	repoOpt := deps.RepoOptF()
	if repoOpt.Worktree == nil {
		return nil, api.AsStatus(codes.Internal, errors.New("filesystem not provided"))
	}
	if repoOpt.Storer == nil {
		return nil, api.AsStatus(codes.Internal, errors.New("git storage not provided"))
	}
	if deps.GitCache != nil {
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, deps.GitCache)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	if deps.CratesRegistryStub != nil {
		ctx = context.WithValue(ctx, rebuild.CratesRegistryStubID, deps.CratesRegistryStub)
	}
	mux := meta.NewRegistryMux(deps.HTTPClient)
	var s rebuild.Strategy
	t := rebuild.Target{
		Ecosystem: req.Ecosystem,
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}
	rebuilder, ok := meta.AllRebuilders[req.Ecosystem]
	if !ok {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	s, rcfg, err := doInfer(ctx, rebuilder, t, mux, req.LocationHint(), repoOpt)
	// TODO: Move before doInfer so we record metrics even when inference fails.
	if deps.RepoMetrics != nil && rcfg.Repository != nil {
		if err := recordRepoMetrics(ctx, deps.RepoMetrics, rcfg); err != nil {
			log.Printf("repo_metrics: %s: %v", rcfg.URI, err)
		}
	}
	if err != nil {
		log.Printf("No inference for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, err)
		err = errors.Wrap(err, "failed to infer strategy")
		var located *rebuild.InferenceError
		if errors.As(err, &located) {
			// The caller can still check out the source the inference reached.
			return nil, api.AsStatus(codes.Internal, err, api.AsDetail(located.Detail))
		}
		return nil, api.AsStatus(codes.Internal, err)
	}
	return new(schema.NewStrategyOneOf(s)), nil
}
