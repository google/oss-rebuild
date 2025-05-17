// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"bytes"
	"context"
	"crypto"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/cache"
	internalExecutor "github.com/google/oss-rebuild/internal/executor"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/debian"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
	"io"
	"log"
	"net/http"
)

type localExecutionService struct {
	prebuildURL string
	store       rebuild.LocatableAssetStore
	logsink     io.Writer
	containers  map[string]internalExecutor.DockerExecutor
}

func NewLocalExecutionService(prebuildURL string, store rebuild.LocatableAssetStore, logsink io.Writer) ExecutionService {
	return &localExecutionService{prebuildURL: prebuildURL, store: store, logsink: logsink, containers: make(map[string]internalExecutor.DockerExecutor)}
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
	regClient := httpx.NewCachedClient(http.DefaultClient, &cache.CoalescingMemoryCache{})
	mux := rebuild.RegistryMux{
		Debian:   debianreg.HTTPRegistry{Client: regClient},
		CratesIO: cratesreg.HTTPRegistry{Client: regClient},
		NPM:      npmreg.HTTPRegistry{Client: regClient},
		PyPI:     pypireg.HTTPRegistry{Client: regClient},
	}
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version, Artifact: req.Artifact}
	verdict := &schema.Verdict{Target: t}
	if req.Artifact == "" {
		switch t.Ecosystem {
		case rebuild.NPM:
			t.Artifact = npm.ArtifactName(t)
		case rebuild.PyPI:
			release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
			if err != nil {
				verdict.Message = err.Error()
				return verdict, nil
				//return nil, errors.Wrap(err, "fetching pypi release")
			}
			wheel, err := pypi.FindPureWheel(release.Artifacts)
			if err != nil {
				// TODO: requires PR #468
				//wheel, err = pypi.FindSourceDistribution(release.Artifacts)
				//if err != nil {
				verdict.Message = err.Error()
				return verdict, nil
				//}
			}
			t.Artifact = wheel.Filename
		case rebuild.CratesIO:
			t.Artifact = cratesio.ArtifactName(t)
		case rebuild.Debian:
			return nil, errors.New("artifact name required")
		case rebuild.Maven:
			return nil, errors.New("maven not implemented")
		default:
			return nil, errors.New("unsupported ecosystem")
		}
	}
	verdict.Target.Artifact = t.Artifact
	strategy, err := s.infer(ctx, t, mux)
	if err != nil {
		verdict.Message = err.Error()
		return verdict, nil
	}
	verdict.StrategyOneof = schema.NewStrategyOneOf(strategy)
	var comparisonOutcome error
	if err := s.build(ctx, t, strategy, s.store, buildOpts{PrebuildURL: s.prebuildURL, LogSink: s.logsink}); err != nil {
		verdict.Message = err.Error()
	} else if comparisonOutcome, err = compare(ctx, t, s.store, mux); err != nil {
		verdict.Message = err.Error()
	} else if comparisonOutcome != nil {
		verdict.Message = comparisonOutcome.Error()
	}

	return verdict, nil
}

func (s *localExecutionService) infer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (rebuild.Strategy, error) {
	mem := memory.NewStorage()
	fs := memfs.New()
	var rebuilder rebuild.Rebuilder
	switch t.Ecosystem {
	case rebuild.NPM:
		rebuilder = npm.Rebuilder{}
	case rebuild.PyPI:
		rebuilder = pypi.Rebuilder{}
	case rebuild.CratesIO:
		rebuilder = cratesio.Rebuilder{}
	case rebuild.Debian:
		rebuilder = debian.Rebuilder{}
	case rebuild.Maven:
		return nil, errors.New("maven not implemented")
	default:
		return nil, errors.New("unsupported ecosystem")
	}
	repo, err := rebuilder.InferRepo(ctx, t, mux)
	if err != nil {
		return nil, err
	}
	rcfg, err := rebuilder.CloneRepo(ctx, t, repo, fs, mem)
	if err != nil {
		return nil, err
	}
	return rebuilder.InferStrategy(ctx, t, mux, &rcfg, nil)
}

type buildOpts struct {
	PrebuildURL string
	LogSink     io.Writer
}

func (s *localExecutionService) build(ctx context.Context, t rebuild.Target, strategy rebuild.Strategy, out rebuild.LocatableAssetStore, opts buildOpts) error {
	inst, err := strategy.GenerateFor(t, rebuild.BuildEnv{TimewarpHost: "localhost:8081"})
	if err != nil {
		return errors.Wrap(err, "failed generating strategy")
	}

	var outbuffer bytes.Buffer
	var outb io.Reader = &outbuffer

	container, ok := s.containers[t.Package]
	if ok {
		if outbuffer, err = container.ExecuteWithStrategy(ctx, inst, t, out); err != nil {
			return errors.Wrap(err, "failed to execute build with strategy")
		}
	} else {
		newContainer, err := internalExecutor.NewDockerExecutor(t.Package)
		if err != nil {
			return errors.Wrap(err, "failed to create DockerExecutor")
		}

		if err := newContainer.StartContainer(ctx, inst); err != nil {
			return errors.Wrap(err, "failed to start container")
		}
		s.containers[t.Package] = *newContainer
		// Execute the build inside the container
		if outbuffer, err = newContainer.ExecuteWithStrategy(ctx, inst, t, out); err != nil {
			return errors.Wrap(err, "failed to execute build with strategy")
		}
	}

	logw, err := out.Writer(ctx, rebuild.DebugLogsAsset.For(t))
	if err != nil {
		return err
	}
	defer logw.Close()

	if _, err := io.Copy(logw, outb); err != nil {
		return err
	}

	return nil
}

// returns True, nil for exactMatch, returns False, nil for stabilizedMatch
func compare(ctx context.Context, t rebuild.Target, store rebuild.LocatableAssetStore, mux rebuild.RegistryMux) (error, error) {
	if _, err := store.Reader(ctx, rebuild.RebuildAsset.For(t)); err != nil {
		return nil, errors.Wrap(err, "accessing rebuild artifact")
	}
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return nil, errors.Wrap(err, "getting stabilizers")
	}
	var upstreamURL string
	switch t.Ecosystem {
	case rebuild.NPM:
		vmeta, err := mux.NPM.Version(ctx, t.Package, t.Version)
		if err != nil {
			return nil, errors.Wrap(err, "fetching npm metadata")
		}
		upstreamURL = vmeta.Dist.URL
	case rebuild.PyPI:
		release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
		if err != nil {
			return nil, errors.Wrap(err, "fetching pypi metadata")
		}
		for _, r := range release.Artifacts {
			if r.Filename == t.Artifact {
				upstreamURL = r.URL
				break
			}
		}
		if upstreamURL == "" {
			return nil, errors.Errorf("artifact %s not found in release", t.Artifact)
		}
	case rebuild.CratesIO:
		vmeta, err := mux.CratesIO.Version(ctx, t.Package, t.Version)
		if err != nil {
			return nil, errors.Wrap(err, "fetching crates.io metadata")
		}
		upstreamURL = vmeta.DownloadURL
	case rebuild.Debian:
		_, name, err := debian.ParseComponent(t.Package)
		if err != nil {
			return nil, errors.Wrap(err, "parsing debian component")
		}
		upstreamURL, err = mux.Debian.ArtifactURL(ctx, name, t.Artifact)
		if err != nil {
			return nil, errors.Wrap(err, "getting debian artifact URL")
		}
	case rebuild.Maven:
		return nil, errors.New("maven comparison not implemented")
	default:
		return nil, errors.Errorf("unsupported ecosystem: %s", t.Ecosystem)
	}
	if upstreamURL == "" {
		return nil, errors.New("couldn't determine upstream URL")
	}
	hashes := []crypto.Hash{crypto.SHA256}
	if t.Ecosystem == rebuild.NPM {
		hashes = append(hashes, crypto.SHA512)
	}
	rbSummary, upSummary, err := verifier.SummarizeArtifacts(ctx, store, t, upstreamURL, hashes, stabilizers)
	if err != nil {
		return nil, errors.Wrap(err, "summarizing artifacts")
	}
	exactMatch := bytes.Equal(rbSummary.Hash.Sum(nil), upSummary.Hash.Sum(nil))
	stabilizedMatch := bytes.Equal(rbSummary.StabilizedHash.Sum(nil), upSummary.StabilizedHash.Sum(nil))
	if exactMatch {
		log.Printf("Exact match found for %s %s %s", t.Ecosystem, t.Package, t.Artifact)
		return errors.New("Exact match"), nil
	}
	if stabilizedMatch {
		log.Printf("Stabilized match found for %s %s %s", t.Ecosystem, t.Package, t.Artifact)
		return errors.New("Stabilized match"), nil
	}

	// TODO: Code duplication with SummarizeArtifacts
	req, _ := http.NewRequest(http.MethodGet, upSummary.URI, nil)
	resp, err := rebuild.DoContext(ctx, req)
	if err != nil {
		return nil, errors.Wrap(err, "fetching upstream artifact")
	}
	if resp.StatusCode != 200 {
		return nil, errors.Wrap(errors.New(resp.Status), "fetching upstream artifact")
	}

	file, err := store.Writer(ctx, rebuild.DebugUpstreamAsset.For(t))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create file in assetStore")
	}

	// Copy the file content to the assetStore
	if _, err := io.Copy(file, resp.Body); err != nil {
		return nil, errors.Wrap(err, "failed to write file to assetStore")
	}

	rb, up, err := rebuild.Summarize(ctx, t, rebuild.RebuildAsset.For(t), rebuild.DebugUpstreamAsset.For(t), store)
	verdict, err := pypi.CompareTwoFiles(rb, up)
	return verdict, nil
}

func (s *localExecutionService) SmoketestPackage(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
	var verdicts []schema.Verdict

	for _, ver := range req.Versions {

		rebReq := schema.RebuildPackageRequest{
			Ecosystem: req.Ecosystem,
			Package:   req.Package,
			Version:   ver,
			ID:        req.ID,
		}

		reb, err := s.RebuildPackage(ctx, rebReq)
		if err != nil {
			// cannot find the artifact
			verdicts = append(verdicts, schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebReq.Ecosystem,
					Package:   rebReq.Package,
					Version:   rebReq.Version},
				Message: err.Error()})
			//return nil, errors.Wrap(err, "rebuilding package")
		} else {
			verdicts = append(verdicts, *reb)
		}
	}

	for _, c := range s.containers {
		err := c.StopContainer(ctx)
		if err != nil {
			log.Printf("failed to stop container: %v", err)
		}
	}

	return &schema.SmoketestResponse{
		Verdicts: verdicts,
		Executor: "docker",
	}, nil
	//return nil, errors.New("Not implemented")
}

func (s *localExecutionService) Warmup(ctx context.Context) { /* no-op */ }
