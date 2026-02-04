// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"bytes"
	"context"
	"crypto"
	"io"
	"log"
	"net/http"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
	"github.com/google/oss-rebuild/pkg/rebuild/debian"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/pkg/errors"
)

type localExecutionService struct {
	prebuildURL string
	store       rebuild.LocatableAssetStore
	logsink     io.Writer
	gitCache    *gitx.Cache
}

type LocalExecutionServiceConfig struct {
	PrebuildURL string
	Store       rebuild.LocatableAssetStore
	LogSink     io.Writer
	GitCache    *gitx.Cache
}

func NewLocalExecutionService(config LocalExecutionServiceConfig) ExecutionService {
	return &localExecutionService{prebuildURL: config.PrebuildURL, store: config.Store, logsink: config.LogSink, gitCache: config.GitCache}
}

func (s *localExecutionService) RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest) (*schema.Verdict, error) {
	if req.UseRepoDefinition {
		return nil, errors.New("repo-based definitions not supported")
	}
	if req.UseNetworkProxy {
		return nil, errors.New("network proxy not supported")
	}
	if req.UseSyscallMonitor {
		return nil, errors.New("syscall monitor not supported")
	}
	mux := meta.NewRegistryMux(httpx.NewCachedClient(http.DefaultClient, &cache.CoalescingMemoryCache{}))
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version, Artifact: req.Artifact}
	if req.Artifact == "" {
		a, err := meta.GuessArtifact(ctx, t, mux)
		if err != nil {
			return nil, errors.Wrap(err, "selecting artifact")
		}
		t.Artifact = a
	}
	verdict := &schema.Verdict{Target: t}
	strategy, err := s.infer(ctx, t, mux)
	if err != nil {
		verdict.Message = err.Error()
		return verdict, nil
	}
	verdict.StrategyOneof = schema.NewStrategyOneOf(strategy)
	if err := executeBuild(ctx, t, strategy, s.store, buildOpts{PrebuildURL: s.prebuildURL, LogSink: s.logsink}); err != nil {
		verdict.Message = err.Error()
	} else if err := compare(ctx, t, s.store, mux); err != nil {
		verdict.Message = err.Error()
	}
	return verdict, nil
}

// RebuildWithStrategy rebuilds a package using a provided strategy instead of inferring one.
// This is only supported for local execution services.
func RebuildWithStrategy(ctx context.Context, executor ExecutionService, t rebuild.Target, strategy rebuild.Strategy) (*schema.Verdict, error) {
	local, ok := executor.(*localExecutionService)
	if !ok {
		return nil, errors.New("RebuildWithStrategy is only supported for local execution")
	}
	mux := meta.NewRegistryMux(httpx.NewCachedClient(http.DefaultClient, &cache.CoalescingMemoryCache{}))
	if t.Artifact == "" {
		a, err := meta.GuessArtifact(ctx, t, mux)
		if err != nil {
			return nil, errors.Wrap(err, "selecting artifact")
		}
		t.Artifact = a
	}
	verdict := &schema.Verdict{Target: t}
	verdict.StrategyOneof = schema.NewStrategyOneOf(strategy)
	if err := executeBuild(ctx, t, strategy, local.store, buildOpts{PrebuildURL: local.prebuildURL, LogSink: local.logsink}); err != nil {
		verdict.Message = err.Error()
	} else if err := compare(ctx, t, local.store, mux); err != nil {
		verdict.Message = err.Error()
	}
	return verdict, nil
}

func (s *localExecutionService) infer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (rebuild.Strategy, error) {
	rebuilder, ok := meta.AllRebuilders[t.Ecosystem]
	if !ok {
		return nil, errors.New("unsupported ecosystem")
	}
	if s.gitCache != nil {
		ctx = context.WithValue(ctx, rebuild.RepoCacheClientID, s.gitCache)
	}
	repo, err := rebuilder.InferRepo(ctx, t, mux)
	if err != nil {
		return nil, err
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, &gitx.RepositoryOptions{Worktree: memfs.New(), Storer: memory.NewStorage()})
	if err != nil {
		return nil, err
	}
	return rebuilder.InferStrategy(ctx, t, mux, &rcfg, nil)
}

func (s *localExecutionService) Infer(ctx context.Context, req schema.InferenceRequest) (*schema.StrategyOneOf, error) {
	if req.StrategyHint != nil {
		return nil, errors.New("strategy hint not supported")
	}
	mux := meta.NewRegistryMux(httpx.NewCachedClient(http.DefaultClient, &cache.CoalescingMemoryCache{}))
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version, Artifact: req.Artifact}
	if req.Artifact == "" {
		a, err := meta.GuessArtifact(ctx, t, mux)
		if err != nil {
			return nil, errors.Wrap(err, "selecting artifact")
		}
		t.Artifact = a
	}
	strategy, err := s.infer(ctx, t, mux)
	if err != nil {
		return nil, err
	}
	strat := schema.NewStrategyOneOf(strategy)
	return &strat, nil
}

type buildOpts struct {
	PrebuildURL string
	LogSink     io.Writer
}

func executeBuild(ctx context.Context, t rebuild.Target, strategy rebuild.Strategy, out rebuild.LocatableAssetStore, opts buildOpts) error {
	executor, err := local.NewDockerRunExecutor(local.DockerRunExecutorConfig{
		Planner:     local.NewDockerRunPlanner(),
		MaxParallel: 1,
	})
	if err != nil {
		return errors.Wrap(err, "failed to create executor")
	}
	defer executor.Close(ctx)
	input := rebuild.Input{
		Target:   t,
		Strategy: strategy,
	}
	buildOpts := build.Options{
		Resources: build.Resources{
			AssetStore: out,
			ToolURLs: map[build.ToolType]string{
				build.TimewarpTool: opts.PrebuildURL + "/timewarp",
			},
			BaseImageConfig: build.DefaultBaseImageConfig(),
		},
		UseTimewarp: meta.AllRebuilders[t.Ecosystem].UsesTimewarp(input),
	}
	handle, err := executor.Start(ctx, input, buildOpts)
	if err != nil {
		return errors.Wrap(err, "failed to start build")
	}
	if opts.LogSink != nil {
		go io.Copy(opts.LogSink, handle.OutputStream())
	}
	result, err := handle.Wait(ctx)
	if err != nil {
		return err
	}
	if result.Error != nil {
		return errors.Wrap(result.Error, "build failed")
	}
	return nil
}

func compare(ctx context.Context, t rebuild.Target, store rebuild.LocatableAssetStore, mux rebuild.RegistryMux) error {
	if _, err := store.Reader(ctx, rebuild.RebuildAsset.For(t)); err != nil {
		return errors.Wrap(err, "accessing rebuild artifact")
	}
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return errors.Wrap(err, "getting stabilizers")
	}
	var upstreamURL string
	switch t.Ecosystem {
	case rebuild.NPM:
		vmeta, err := mux.NPM.Version(ctx, t.Package, t.Version)
		if err != nil {
			return errors.Wrap(err, "fetching npm metadata")
		}
		upstreamURL = vmeta.Dist.URL
	case rebuild.PyPI:
		release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
		if err != nil {
			return errors.Wrap(err, "fetching pypi metadata")
		}
		for _, r := range release.Artifacts {
			if r.Filename == t.Artifact {
				upstreamURL = r.URL
				break
			}
		}
		if upstreamURL == "" {
			return errors.Errorf("artifact %s not found in release", t.Artifact)
		}
	case rebuild.CratesIO:
		vmeta, err := mux.CratesIO.Version(ctx, t.Package, t.Version)
		if err != nil {
			return errors.Wrap(err, "fetching crates.io metadata")
		}
		upstreamURL = vmeta.DownloadURL
	case rebuild.Debian:
		_, name, err := debian.ParseComponent(t.Package)
		if err != nil {
			return errors.Wrap(err, "parsing debian component")
		}
		upstreamURL, err = mux.Debian.ArtifactURL(ctx, name, t.Artifact)
		if err != nil {
			return errors.Wrap(err, "getting debian artifact URL")
		}
	case rebuild.Maven:
		upstreamURL, err = mux.Maven.ReleaseURL(ctx, t.Package, t.Version, maven.TypeJar)
		if err != nil {
			return errors.Wrap(err, "getting maven artifact URL")
		}
	default:
		return errors.Errorf("unsupported ecosystem: %s", t.Ecosystem)
	}
	if upstreamURL == "" {
		return errors.New("couldn't determine upstream URL")
	}
	hashes := []crypto.Hash{crypto.SHA256}
	if t.Ecosystem == rebuild.NPM {
		hashes = append(hashes, crypto.SHA512)
	}
	rbSummary, upSummary, err := verifier.SummarizeArtifacts(ctx, store, t, upstreamURL, hashes, stabilizers)
	if err != nil {
		return errors.Wrap(err, "summarizing artifacts")
	}
	exactMatch := bytes.Equal(rbSummary.Hash.Sum(nil), upSummary.Hash.Sum(nil))
	stabilizedMatch := bytes.Equal(rbSummary.StabilizedHash.Sum(nil), upSummary.StabilizedHash.Sum(nil))
	if exactMatch {
		log.Printf("Exact match found for %s %s %s", t.Ecosystem, t.Package, t.Artifact)
		return nil
	}
	if stabilizedMatch {
		log.Printf("Stabilized match found for %s %s %s", t.Ecosystem, t.Package, t.Artifact)
		return nil
	}
	return errors.New("rebuild does not match upstream artifact")
}

func (s *localExecutionService) Warmup(ctx context.Context) { /* no-op */ }
