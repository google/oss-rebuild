// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/builddef"
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	debianrb "github.com/google/oss-rebuild/pkg/rebuild/debian"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	debianreg "github.com/google/oss-rebuild/pkg/registry/debian"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/grpc/codes"
)

func doDebianRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	_, name, err := debianrb.ParseComponent(t.Package)
	if err != nil {
		return "", err
	}
	if err := debianrb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	return mux.Debian.ArtifactURL(ctx, name, t.Artifact)
}

func doNPMRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	if err := npmrb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	vmeta, err := mux.NPM.Version(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	return vmeta.Dist.URL, nil
}

func doCratesRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	if err := cratesrb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	vmeta, err := mux.CratesIO.Version(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	return vmeta.DownloadURL, nil
}

func doPyPIRebuild(ctx context.Context, t rebuild.Target, id string, mux rebuild.RegistryMux, s rebuild.Strategy, opts rebuild.RemoteOptions) (upstreamURL string, err error) {
	release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
	if err != nil {
		return "", errors.Wrap(err, "fetching metadata failed")
	}
	for _, r := range release.Artifacts {
		if r.Filename == t.Artifact {
			upstreamURL = r.URL
		}
	}
	if upstreamURL == "" {
		return "", errors.New("artifact not found in release")
	}
	if err := pypirb.RebuildRemote(ctx, rebuild.Input{Target: t, Strategy: s}, id, opts); err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	return upstreamURL, nil
}

func sanitize(key string) string {
	return strings.ReplaceAll(key, "/", "!")
}

func populateArtifact(ctx context.Context, t *rebuild.Target, mux rebuild.RegistryMux) error {
	if t.Artifact != "" {
		return nil
	}
	switch t.Ecosystem {
	case rebuild.NPM:
		t.Artifact = npmrb.ArtifactName(*t)
	case rebuild.CratesIO:
		t.Artifact = cratesrb.ArtifactName(*t)
	case rebuild.PyPI:
		release, err := mux.PyPI.Release(ctx, t.Package, t.Version)
		if err != nil {
			return errors.Wrap(err, "fetching metadata failed")
		}
		a, err := pypirb.FindPureWheel(release.Artifacts)
		if err != nil {
			return errors.Wrap(err, "locating pure wheel failed")
		}
		t.Artifact = a.Filename
	case rebuild.Debian:
		return errors.New("debian requires explicit artifact")
	default:
		return errors.New("unknown ecosystem")
	}
	return nil
}

// TODO: LocalMetadataStore and DebugStoreBuilder can be combined into a layered AssetStore.
type RebuildPackageDeps struct {
	HTTPClient                 httpx.BasicClient
	FirestoreClient            *firestore.Client
	Signer                     *dsse.EnvelopeSigner
	GCBClient                  gcb.Client
	BuildProject               string
	BuildServiceAccount        string
	UtilPrebuildBucket         string
	UtilPrebuildAuth           bool
	BuildLogsBucket            string
	ServiceRepo                rebuild.Location
	PrebuildRepo               rebuild.Location
	BuildDefRepo               rebuild.Location
	PublishForLocalServiceRepo bool
	AttestationStore           rebuild.AssetStore
	LocalMetadataStore         rebuild.AssetStore
	DebugStoreBuilder          func(ctx context.Context) (rebuild.AssetStore, error)
	RemoteMetadataStoreBuilder func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error)
	OverwriteAttestations      bool
	InferStub                  api.StubT[schema.InferenceRequest, schema.StrategyOneOf]
}

type repoEntry struct {
	// BuildDefinition found in the build def repo.
	schema.BuildDefinition
	// BuildDefLoc is the repo where the build def was accessed.
	BuildDefLoc rebuild.Location
}

// getStrategy determines which strategy we should execute. If a build def repo was used, that data will be included as repoEntry.
func getStrategy(ctx context.Context, deps *RebuildPackageDeps, t rebuild.Target, fromRepo bool) (rebuild.Strategy, *repoEntry, error) {
	var strategy rebuild.Strategy
	var entry *repoEntry
	ireq := schema.InferenceRequest{
		Ecosystem: t.Ecosystem,
		Package:   t.Package,
		Version:   t.Version,
		Artifact:  t.Artifact,
	}
	if fromRepo {
		var sparseDirs []string
		if deps.BuildDefRepo.Dir != "." {
			sparseDirs = append(sparseDirs, deps.BuildDefRepo.Dir)
		}
		defs, err := builddef.NewBuildDefinitionSetFromGit(&builddef.GitBuildDefinitionSetOptions{
			CloneOptions: git.CloneOptions{
				URL:           deps.BuildDefRepo.Repo,
				ReferenceName: plumbing.ReferenceName(deps.BuildDefRepo.Ref),
				Depth:         1,
				NoCheckout:    true,
			},
			RelativePath: deps.BuildDefRepo.Dir,
			// TODO: Limit this further to only the target's path we want.
			SparseCheckoutDirs: sparseDirs,
		})
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating build definition repo reader")
		}
		pth, _ := defs.Path(ctx, t)
		entry = &repoEntry{
			BuildDefLoc: rebuild.Location{
				Repo: deps.BuildDefRepo.Repo,
				Ref:  defs.Ref().String(),
				Dir:  pth,
			},
		}
		entry.BuildDefinition, err = defs.Get(ctx, t)
		if err != nil {
			return nil, nil, errors.Wrap(err, "accessing build definition")
		}
		if entry.BuildDefinition.StrategyOneOf != nil {
			defnStrategy, err := entry.BuildDefinition.Strategy()
			if err != nil {
				return nil, nil, errors.Wrap(err, "accessing strategy")
			}
			if hint, ok := defnStrategy.(*rebuild.LocationHint); ok && hint != nil {
				ireq.StrategyHint = &schema.StrategyOneOf{LocationHint: hint}
			} else {
				strategy = defnStrategy
			}
		}
	}
	if strategy == nil {
		s, err := deps.InferStub(ctx, ireq)
		if err != nil {
			// TODO: Surface better error than Internal.
			return nil, nil, errors.Wrap(err, "fetching inference")
		}
		strategy, err = s.Strategy()
		if err != nil {
			return nil, nil, errors.Wrap(err, "reading strategy")
		}
	}
	return strategy, entry, nil
}

func buildAndAttest(ctx context.Context, deps *RebuildPackageDeps, mux rebuild.RegistryMux, a verifier.Attestor, t rebuild.Target, strategy rebuild.Strategy, entry *repoEntry, useProxy bool, useSyscallMonitor bool) (err error) {
	debugStore, err := deps.DebugStoreBuilder(ctx)
	if err != nil {
		return errors.Wrap(err, "creating debug store")
	}
	id := uuid.New().String()
	remoteMetadata, err := deps.RemoteMetadataStoreBuilder(ctx, id)
	if err != nil {
		return errors.Wrap(err, "creating rebuild store")
	}
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return errors.Wrap(err, "getting stabilizers for target")
	}
	if entry != nil && len(entry.BuildDefinition.CustomStabilizers) > 0 {
		customStabilizers, err := archive.CreateCustomStabilizers(entry.BuildDefinition.CustomStabilizers, t.ArchiveType())
		if err != nil {
			return errors.Wrap(err, "creating stabilizers")
		}
		stabilizers = append(stabilizers, customStabilizers...)
	}
	var buildDefRepo rebuild.Location
	var buildDef *schema.BuildDefinition
	if entry != nil {
		buildDefRepo = entry.BuildDefLoc
		buildDef = &entry.BuildDefinition
	}
	hashes := []crypto.Hash{crypto.SHA256}
	opts := rebuild.RemoteOptions{
		GCBClient:           deps.GCBClient,
		Project:             deps.BuildProject,
		BuildServiceAccount: deps.BuildServiceAccount,
		UtilPrebuildBucket:  deps.UtilPrebuildBucket,
		UtilPrebuildDir:     deps.PrebuildRepo.Ref,
		UtilPrebuildAuth:    deps.UtilPrebuildAuth,
		LogsBucket:          deps.BuildLogsBucket,
		LocalMetadataStore:  deps.LocalMetadataStore,
		DebugStore:          debugStore,
		RemoteMetadataStore: remoteMetadata,
		UseSyscallMonitor:   useSyscallMonitor,
		UseNetworkProxy:     useProxy,
	}
	var upstreamURI string
	switch t.Ecosystem {
	case rebuild.NPM:
		hashes = append(hashes, crypto.SHA512)
		upstreamURI, err = doNPMRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.CratesIO:
		upstreamURI, err = doCratesRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.PyPI:
		upstreamURI, err = doPyPIRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.Debian:
		upstreamURI, err = doDebianRebuild(ctx, t, id, mux, strategy, opts)
	default:
		return api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	if err != nil {
		return errors.Wrap(err, "rebuilding")
	}
	rb, up, err := verifier.SummarizeArtifacts(ctx, remoteMetadata, t, upstreamURI, hashes, stabilizers)
	if err != nil {
		return errors.Wrap(err, "comparing artifacts")
	}
	exactMatch := bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	stabilizedMatch := bytes.Equal(rb.StabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil))
	if !exactMatch && !stabilizedMatch {
		return api.AsStatus(codes.FailedPrecondition, errors.New("rebuild content mismatch"))
	}
	if u, err := url.Parse(deps.ServiceRepo.Repo); err != nil {
		return errors.Wrap(err, "bad ServiceRepo URL")
	} else if (u.Scheme == "file" || u.Scheme == "") && !deps.PublishForLocalServiceRepo {
		return errors.Wrap(err, "disallowed file:// ServiceRepo URL")
	}
	eqStmt, buildStmt, err := verifier.CreateAttestations(ctx, t, buildDef, strategy, id, rb, up, deps.LocalMetadataStore, deps.ServiceRepo, deps.PrebuildRepo, buildDefRepo)
	if err != nil {
		return errors.Wrap(err, "creating attestations")
	}
	if err := a.PublishBundle(ctx, t, eqStmt, buildStmt); err != nil {
		return errors.Wrap(err, "publishing bundle")
	}
	return nil
}

func rebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*schema.Verdict, error) {
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version, Artifact: req.Artifact}
	if req.Ecosystem == rebuild.Debian && strings.TrimSpace(req.Artifact) == "" {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("debian requires artifact"))
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	regclient := httpx.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{})
	mux := rebuild.RegistryMux{
		Debian:   debianreg.HTTPRegistry{Client: regclient},
		CratesIO: cratesreg.HTTPRegistry{Client: regclient},
		NPM:      npmreg.HTTPRegistry{Client: regclient},
		PyPI:     pypireg.HTTPRegistry{Client: regclient},
	}
	if err := populateArtifact(ctx, &t, mux); err != nil {
		// If we fail to populate artifact, the verdict has an incomplete target, which might prevent the storage of the verdict.
		// For this reason, we don't return a nil error and expect no verdict to be written.
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "selecting artifact"))
	}
	v := schema.Verdict{
		Target: t,
	}
	signer := verifier.InTotoEnvelopeSigner{EnvelopeSigner: deps.Signer}
	a := verifier.Attestor{Store: deps.AttestationStore, Signer: signer, AllowOverwrite: deps.OverwriteAttestations}
	if !deps.OverwriteAttestations {
		if exists, err := a.BundleExists(ctx, t); err != nil {
			v.Message = errors.Wrap(err, "checking existing bundle").Error()
			return &v, nil
		} else if exists {
			v.Message = api.AsStatus(codes.AlreadyExists, errors.New("conflict with existing attestation bundle")).Error()
			return &v, nil
		}
	}
	strategy, entry, err := getStrategy(ctx, deps, t, req.UseRepoDefinition)
	if err != nil {
		v.Message = errors.Wrap(err, "getting strategy").Error()
		return &v, nil
	}
	if strategy != nil {
		v.StrategyOneof = schema.NewStrategyOneOf(strategy)
	}
	err = buildAndAttest(ctx, deps, mux, a, t, strategy, entry, req.UseNetworkProxy, req.UseSyscallMonitor)
	if err != nil {
		v.Message = errors.Wrap(err, "executing rebuild").Error()
		return &v, nil
	}
	return &v, nil
}

func RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*schema.Verdict, error) {
	started := time.Now().UTC()
	ctx = context.WithValue(ctx, rebuild.RunID, req.ID)
	var timeout time.Duration
	if req.BuildTimeout == 0 {
		// Cloud Run times out after 60 minutes, give ourselves 5 minutes to cleanup and log results.
		timeout = 55 * time.Minute
	} else {
		timeout = req.BuildTimeout
	}
	ctx = context.WithValue(ctx, rebuild.GCBDeadlineID, time.Now().Add(timeout))
	v, err := rebuildPackage(ctx, req, deps)
	if err != nil {
		return nil, err
	}
	var dockerfile string
	r, err := deps.LocalMetadataStore.Reader(ctx, rebuild.DockerfileAsset.For(v.Target))
	if err == nil {
		if b, err := io.ReadAll(r); err == nil {
			dockerfile = string(b)
		} else {
			log.Println("Failed to load dockerfile:", err)
		}
	}
	var bi rebuild.BuildInfo
	r, err = deps.LocalMetadataStore.Reader(ctx, rebuild.BuildInfoAsset.For(v.Target))
	if err == nil {
		if err = json.NewDecoder(r).Decode(&bi); err != nil {
			log.Println("Failed to load build info:", err)
		}
	}
	_, err = deps.FirestoreClient.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(v.Target.Package)).Collection("versions").Doc(v.Target.Version).Collection("artifacts").Doc(v.Target.Artifact).Collection("attempts").Doc(req.ID).Set(ctx, schema.RebuildAttempt{
		Ecosystem:       string(v.Target.Ecosystem),
		Package:         v.Target.Package,
		Version:         v.Target.Version,
		Artifact:        v.Target.Artifact,
		Success:         v.Message == "",
		Message:         v.Message,
		Strategy:        v.StrategyOneof,
		Dockerfile:      dockerfile,
		ExecutorVersion: os.Getenv("K_REVISION"),
		RunID:           req.ID,
		BuildID:         bi.BuildID,
		ObliviousID:     bi.ID,
		Started:         started,
		Created:         time.Now().UTC(),
	})
	if err != nil {
		log.Print(errors.Wrap(err, "storing results in firestore"))
	}
	return v, nil
}
