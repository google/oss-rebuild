// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"context"
	"log"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

func doInfer(ctx context.Context, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux, hint rebuild.Strategy, s storage.Storer, fs billy.Filesystem) (rebuild.Strategy, error) {
	var repo string
	if lh, ok := hint.(*rebuild.LocationHint); ok && lh != nil {
		repo = lh.Location.Repo
	} else {
		var err error
		repo, err = rebuilder.InferRepo(ctx, t, mux)
		if err != nil {
			return nil, err
		}
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, fs, s)
	if err != nil {
		return nil, err
	}
	strategy, err := rebuilder.InferStrategy(ctx, t, mux, &rcfg, hint)
	if err != nil {
		return nil, err
	}
	return strategy, nil
}

type InferDeps struct {
	HTTPClient httpx.BasicClient
	GitCache   *gitx.Cache
	StorageF   func() storage.Storer
	WorktreeF  func() billy.Filesystem
}

func Infer(ctx context.Context, req schema.InferenceRequest, deps *InferDeps) (*schema.StrategyOneOf, error) {
	if req.LocationHint() != nil && req.LocationHint().Ref == "" && req.LocationHint().Dir != "" {
		return nil, api.AsStatus(codes.Unimplemented, errors.New("location hint dir without ref not implemented"))
	}
	wt := deps.WorktreeF()
	if wt == nil {
		return nil, api.AsStatus(codes.Internal, errors.New("filesystem not provided"))
	}
	store := deps.StorageF()
	if store == nil {
		return nil, api.AsStatus(codes.Internal, errors.New("git storage not provided"))
	}
	if deps.GitCache != nil {
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, *deps.GitCache)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: deps.HTTPClient},
		NPM:      npmreg.HTTPRegistry{Client: deps.HTTPClient},
		PyPI:     pypireg.HTTPRegistry{Client: deps.HTTPClient},
		Maven:    mavenreg.HTTPRegistry{Client: deps.HTTPClient},
		Debian:   debianreg.HTTPRegistry{Client: deps.HTTPClient},
	}
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
	s, err := doInfer(ctx, rebuilder, t, mux, req.LocationHint(), store, wt)
	if err != nil {
		log.Printf("No inference for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, err)
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "failed to infer strategy"))
	}
	oneof := schema.NewStrategyOneOf(s)
	return &oneof, nil
}
