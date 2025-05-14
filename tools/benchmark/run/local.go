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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/cache"
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
)

type localExecutionService struct {
	prebuildURL string
	store       rebuild.LocatableAssetStore
	logsink     io.Writer
}

func NewLocalExecutionService(prebuildURL string, store rebuild.LocatableAssetStore, logsink io.Writer) ExecutionService {
	return &localExecutionService{prebuildURL: prebuildURL, store: store, logsink: logsink}
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
	if req.Artifact == "" {
		switch t.Ecosystem {
		case rebuild.NPM:
			t.Artifact = npm.ArtifactName(t)
		case rebuild.PyPI:
			release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
			if err != nil {
				return nil, errors.Wrap(err, "fetching pypi release")
			}
			wheel, err := pypi.FindPureWheel(release.Artifacts)
			if err != nil {
				return nil, errors.Wrap(err, "locating wheel")
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
	verdict := &schema.Verdict{Target: t}
	strategy, err := s.infer(ctx, t, mux)
	if err != nil {
		verdict.Message = err.Error()
		return verdict, nil
	}
	verdict.StrategyOneof = schema.NewStrategyOneOf(strategy)
	if err := build(ctx, t, strategy, s.store, buildOpts{PrebuildURL: s.prebuildURL, LogSink: s.logsink}); err != nil {
		verdict.Message = err.Error()
	} else if err := compare(ctx, t, s.store, mux); err != nil {
		verdict.Message = err.Error()
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

func build(ctx context.Context, t rebuild.Target, strategy rebuild.Strategy, out rebuild.LocatableAssetStore, opts buildOpts) error {
	inst, err := strategy.GenerateFor(t, rebuild.BuildEnv{TimewarpHost: "localhost:8081"})
	if err != nil {
		return err
	}
	var install, container string
	if t.Ecosystem == rebuild.Debian {
		install = "apt install -y"
		container = "debian:trixie-20250203-slim"
	} else {
		install = "apk add"
		container = "alpine:3.19"
	}
	buf := &bytes.Buffer{}
	// TODO: Use MakeDockerfile along with `docker build` for a more representative run.
	err = template.Must(template.New("").Parse(
		`set -eux
{{.InstallCmd}} curl
curl `+opts.PrebuildURL+`/timewarp > timewarp
chmod +x timewarp
./timewarp -port 8081 &
while ! nc -z localhost 8081;do sleep 1;done
mkdir /src && cd /src
{{.InstallCmd}} {{.SystemDeps}}
{{.Inst.Source}}
{{.Inst.Deps}}
{{.Inst.Build}}
cp /src/{{.Inst.OutputPath}} /out/rebuild`)).Execute(buf, map[string]any{
		"Inst":       inst,
		"InstallCmd": install,
		"SystemDeps": strings.Join(inst.SystemDeps, " "),
	})
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("/tmp", "rebuild*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	logbuf := &bytes.Buffer{}
	var outw io.Writer = logbuf
	if opts.LogSink != nil {
		outw = io.MultiWriter(opts.LogSink, outw)
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "-i", "--rm", "-v", tmp+":/out", container, "sh")
	cmd.Stdin = buf
	cmd.Stdout = outw
	cmd.Stderr = outw
	if err := cmd.Run(); err != nil {
		if logw, err := out.Writer(ctx, rebuild.DebugLogsAsset.For(t)); err == nil {
			defer logw.Close()
			io.Copy(logw, logbuf)
		}
		return err
	}
	logw, err := out.Writer(ctx, rebuild.DebugLogsAsset.For(t))
	if err != nil {
		return err
	}
	defer logw.Close()
	if _, err := io.Copy(logw, logbuf); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(tmp, "rebuild"))
	if err != nil {
		return err
	}
	w, err := out.Writer(ctx, rebuild.RebuildAsset.For(t))
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, f)
	return err
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
		return errors.New("maven comparison not implemented")
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

func (s *localExecutionService) SmoketestPackage(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
	return nil, errors.New("Not implemented")
}

func (s *localExecutionService) Warmup(ctx context.Context) { /* no-op */ }
