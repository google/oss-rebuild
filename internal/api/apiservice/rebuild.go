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
	"path"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/attestation"
	"github.com/google/oss-rebuild/pkg/build"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/builddef"
	"github.com/google/oss-rebuild/pkg/changelog"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/grpc/codes"
)

// TODO: LocalMetadataStore and DebugStoreBuilder can be combined into a layered AssetStore.
type RebuildPackageDeps struct {
	HTTPClient                 httpx.BasicClient
	FirestoreClient            *firestore.Client
	Signer                     *dsse.EnvelopeSigner
	Verifier                   *dsse.EnvelopeVerifier
	GCBExecutor                *buildgcb.Executor
	PrebuildConfig             rebuild.PrebuildConfig
	ServiceRepo                rebuild.Location
	PrebuildRepo               rebuild.Location
	BuildDefRepo               rebuild.Location
	PublishForLocalServiceRepo bool
	AttestationStore           rebuild.AssetStore
	LocalMetadataStore         rebuild.LocatableAssetStore
	DebugStoreBuilder          func(ctx context.Context) (rebuild.AssetStore, error)
	RemoteMetadataStoreBuilder func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error)
	OverwriteAttestations      bool // TODO: Remove in favor of req.OverwriteMode
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

func buildAndAttest(ctx context.Context, deps *RebuildPackageDeps, mux rebuild.RegistryMux, a verifier.Attestor, t rebuild.Target, strategy rebuild.Strategy, entry *repoEntry, useProxy bool, useSyscallMonitor bool, mode schema.OverwriteMode) (err error) {
	debugStore, err := deps.DebugStoreBuilder(ctx)
	if err != nil {
		return errors.Wrap(err, "creating debug store")
	}
	obID := uuid.New().String()
	remoteMetadata, err := deps.RemoteMetadataStoreBuilder(ctx, obID)
	if err != nil {
		return errors.Wrap(err, "creating rebuild store")
	}
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return errors.Wrap(err, "getting stabilizers for target")
	}
	if entry != nil && len(entry.BuildDefinition.CustomStabilizers) > 0 {
		customStabilizers, err := stabilize.CreateCustomStabilizers(entry.BuildDefinition.CustomStabilizers, t.ArchiveType())
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
	rebuilder, ok := meta.AllRebuilders[t.Ecosystem]
	if !ok {
		return api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	toolURLs := map[build.ToolType]string{
		build.TimewarpTool: "gs://" + path.Join(deps.PrebuildConfig.Bucket, deps.PrebuildConfig.Dir, "timewarp"),
		build.GSUtilTool:   "gs://" + path.Join(deps.PrebuildConfig.Bucket, deps.PrebuildConfig.Dir, "gsutil_writeonly"),
	}
	var authRequired []string
	if deps.PrebuildConfig.Auth {
		authRequired = append(authRequired, "gs://"+deps.PrebuildConfig.Bucket)
	}
	buildStore := rebuild.NewMixedAssetStore(map[rebuild.AssetType]rebuild.LocatableAssetStore{
		rebuild.ContainerImageAsset:     remoteMetadata,
		rebuild.PostBuildContainerAsset: remoteMetadata,
		rebuild.RebuildAsset:            remoteMetadata,
		rebuild.TetragonLogAsset:        remoteMetadata,
		rebuild.ProxyNetlogAsset:        remoteMetadata,
		rebuild.DockerfileAsset:         deps.LocalMetadataStore,
		rebuild.BuildInfoAsset:          deps.LocalMetadataStore,
	})
	in := rebuild.Input{
		Target:   t,
		Strategy: strategy,
	}
	h, err := deps.GCBExecutor.Start(ctx, in, build.Options{
		BuildID:            obID,
		UseTimewarp:        meta.AllRebuilders[t.Ecosystem].UsesTimewarp(in),
		UseNetworkProxy:    useProxy,
		UseSyscallMonitor:  useSyscallMonitor,
		SaveContainerImage: true,
		Resources: build.Resources{
			AssetStore:       buildStore,
			ToolURLs:         toolURLs,
			ToolAuthRequired: authRequired,
			BaseImageConfig:  build.DefaultBaseImageConfig(),
		},
	})
	if err != nil {
		return api.AsStatus(codes.Internal, errors.Wrap(err, "starting build"))
	}
	// Even if we fail, try to copy theses assets to the debug store.
	defer func() {
		for _, a := range []rebuild.AssetType{rebuild.DockerfileAsset, rebuild.BuildInfoAsset} {
			rebuild.AssetCopy(ctx, debugStore, deps.LocalMetadataStore, a.For(t))
		}
	}()
	result, err := h.Wait(ctx)
	if err != nil {
		return errors.Wrap(err, "waiting for build")
	} else if result.Error != nil {
		return errors.Wrap(result.Error, "executing rebuild")
	}
	upstreamURI, err := rebuilder.UpstreamURL(ctx, t, mux)
	if err != nil {
		return errors.Wrap(err, "getting upstream url")
	}
	hashes := []crypto.Hash{crypto.SHA256}
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
		return errors.New("disallowed file:// ServiceRepo URL")
	}
	eqStmt, buildStmt, err := verifier.CreateAttestations(ctx, t, buildDef, strategy, obID, rb, up, deps.LocalMetadataStore, deps.ServiceRepo, deps.PrebuildRepo, buildDefRepo, deps.PrebuildConfig, mode)
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
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := meta.NewRegistryMux(httpx.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{}))
	v := schema.Verdict{
		Target: t,
	}
	signer := verifier.InTotoEnvelopeSigner{EnvelopeSigner: deps.Signer}
	a := verifier.Attestor{Store: deps.AttestationStore, Signer: signer, AllowOverwrite: deps.OverwriteAttestations}
	// Check that, if one exists, the existing attestation should be overwritten.
	if exists, err := a.BundleExists(ctx, t); err != nil {
		v.Message = errors.Wrap(err, "checking existing bundle").Error()
		return &v, nil
	} else if exists {
		allow := false
		switch req.OverwriteMode {
		case schema.OverwriteForce:
			allow = true
		case schema.OverwriteServiceUpdate:
			r, err := deps.AttestationStore.Reader(ctx, rebuild.AttestationBundleAsset.For(t))
			if err != nil {
				v.Message = errors.Wrap(err, "reading existing bundle").Error()
				return &v, nil
			}
			defer r.Close()
			bundleData, err := io.ReadAll(r)
			if err != nil {
				v.Message = errors.Wrap(err, "reading bundle data").Error()
				return &v, nil
			}
			bundle, err := attestation.NewBundle(ctx, bundleData, deps.Verifier)
			if err != nil {
				v.Message = errors.Wrap(err, "parsing attestation bundle").Error()
				return &v, nil
			}
			rebuildAtt, err := attestation.FilterForOne[attestation.RebuildAttestation](
				bundle,
				attestation.WithBuildType(attestation.BuildTypeRebuildV01),
			)
			if err != nil {
				v.Message = errors.Wrap(err, "overwrite denied: no rebuild attestation in bundle").Error()
				return &v, nil
			}
			prevVersion := rebuildAtt.Predicate.BuildDefinition.InternalParameters.ServiceSource.Ref
			// NOTE: We could pull the changelog from the repo at head in the future
			// so we should keep ServiceRepo.Ref as the upper limit instead of
			// just comparing the max changelog entry with prevVersion.
			if !changelog.EntryOnInterval(prevVersion, deps.ServiceRepo.Ref) {
				// NOTE: AsStatus is used here even with the immediate coercion to
				// string with the intent of moving this logic into a Stub.
				v.Message = api.AsStatus(codes.AlreadyExists, errors.New("overwrite denied: no service updates found")).Error()
				return &v, nil
			}
			allow = true
		case "":
			allow = false
		default:
			log.Printf("Unknown OverwriteMode: %s", req.OverwriteMode)
			allow = false
		}
		if !allow {
			v.Message = api.AsStatus(codes.AlreadyExists, errors.New("conflict with existing attestation bundle")).Error()
			return &v, nil
		}
		a.AllowOverwrite = true
	} else { // Bundle doesn't exist
		switch req.OverwriteMode {
		case schema.OverwriteForce:
			// Allow overwrite but empty out the value to omit from the attestation.
			req.OverwriteMode = schema.OverwriteMode("")
		case schema.OverwriteServiceUpdate:
			v.Message = api.AsStatus(codes.FailedPrecondition, errors.Wrap(err, "overwrite denied: no attestation to overwrite")).Error()
			return &v, nil
		}
		// NOTE: This ensures racing rebuilds won't result in multiple attestations being written.
		a.AllowOverwrite = false
	}
	strategy, entry, err := getStrategy(ctx, deps, t, req.UseRepoDefinition)
	if err != nil {
		v.Message = errors.Wrap(err, "getting strategy").Error()
		return &v, nil
	}
	if strategy != nil {
		v.StrategyOneof = schema.NewStrategyOneOf(strategy)
	}
	err = buildAndAttest(ctx, deps, mux, a, t, strategy, entry, req.UseNetworkProxy, req.UseSyscallMonitor, req.OverwriteMode)
	if err != nil {
		v.Message = errors.Wrap(err, "executing rebuild").Error()
		return &v, nil
	}
	return &v, nil
}

func RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*schema.Verdict, error) {
	started := time.Now().UTC()
	ctx = context.WithValue(ctx, rebuild.RunID, req.ID)
	// Cloud Run times out after 60 minutes, give ourselves 5 minutes to cleanup and log results.
	ctx = context.WithValue(ctx, rebuild.GCBWaitDeadlineID, time.Now().Add(55*time.Minute))
	if req.BuildTimeout != 0 {
		ctx = context.WithValue(ctx, rebuild.GCBCancelDeadlineID, time.Now().Add(req.BuildTimeout))
	}
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
	// Encode target for Firestore document IDs (handles NPM slashes, etc.)
	et := rebuild.FirestoreTargetEncoding.Encode(v.Target)
	_, err = deps.FirestoreClient.Collection("ecosystem").Doc(string(et.Ecosystem)).Collection("packages").Doc(et.Package).Collection("versions").Doc(et.Version).Collection("artifacts").Doc(et.Artifact).Collection("attempts").Doc(req.ID).Set(ctx, schema.RebuildAttempt{
		Ecosystem:       string(v.Target.Ecosystem),
		Package:         v.Target.Package,
		Version:         v.Target.Version,
		Artifact:        v.Target.Artifact,
		Success:         v.Message == "",
		Message:         v.Message,
		Strategy:        v.StrategyOneof,
		Dockerfile:      dockerfile,
		ExecutorVersion: deps.ServiceRepo.Ref,
		RunID:           req.ID,
		BuildID:         bi.BuildID,
		ObliviousID:     bi.ObliviousID,
		Started:         started,
		Created:         time.Now().UTC(),
	})
	if err != nil {
		log.Print(errors.Wrap(err, "storing results in firestore"))
	}
	return v, nil
}
