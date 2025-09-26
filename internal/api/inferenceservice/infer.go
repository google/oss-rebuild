// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package inferenceservice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

func doInfer(ctx context.Context, rebuilder rebuild.Rebuilder, t rebuild.Target, mux rebuild.RegistryMux, hint rebuild.Strategy, ropt *gitx.RepositoryOptions) (rebuild.Strategy, error) {
	var repo string
	if lh, ok := hint.(*rebuild.LocationHint); ok && lh != nil {
		var err error
		repo, err = uri.CanonicalizeRepoURI(lh.Location.Repo)
		if err != nil {
			return nil, errors.Wrap(err, "canonicalizing repo hint")
		}
	} else {
		var err error
		repo, err = rebuilder.InferRepo(ctx, t, mux)
		if err != nil {
			return nil, err
		}
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, ropt)
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
	RepoOptF   func() *gitx.RepositoryOptions
	LogStore   rebuild.AssetStore
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
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, *deps.GitCache)
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
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
	// Capture logs to both the standard logger and a buffer for upload
	oldLoggerOutput := log.Writer()
	var inferenceLogs bytes.Buffer
	multiWriter := io.MultiWriter(oldLoggerOutput, &inferenceLogs)
	log.SetOutput(multiWriter)
	defer log.SetOutput(oldLoggerOutput)
	// Perform the inference
	s, err := doInfer(ctx, rebuilder, t, mux, req.LocationHint(), repoOpt)
	// Upload logs and strategy if possible
	if deps.LogStore != nil {
		if uploadErr := uploadLogs(ctx, deps.LogStore, rebuild.InferenceLogAsset.For(t), inferenceLogs.Bytes()); uploadErr != nil {
			log.Printf("Failed to upload inference logs for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, uploadErr)
		}
		if err == nil {
			if uploadErr := uploadStrategy(ctx, deps.LogStore, rebuild.InferredStrategyInputs.For(t), s); uploadErr != nil {
				log.Printf("Failed to upload inference definition for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, uploadErr)
			}
		}

	}
	if err != nil {
		log.Printf("No inference for [pkg=%s, version=%v]: %v\n", req.Package, req.Version, err)
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "failed to infer strategy"))
	}
	oneof := schema.NewStrategyOneOf(s)
	return &oneof, nil
}

func uploadLogs(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, content []byte) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	if _, err := writer.Write(content); err != nil {
		return errors.Wrap(err, "failed to write content to asset store")
	}
	return nil
}

func uploadStrategy(ctx context.Context, store rebuild.AssetStore, asset rebuild.Asset, strategy rebuild.Strategy) error {
	writer, err := store.Writer(ctx, asset)
	if err != nil {
		return errors.Wrap(err, "failed to get asset store writer")
	}
	defer writer.Close()
	enc := json.NewEncoder(writer)
	enc.SetIndent("", "  ")
	if err := enc.Encode(strategy); err != nil {
		log.Fatal(errors.Wrap(err, "encoding result"))
	}
	return nil
}
