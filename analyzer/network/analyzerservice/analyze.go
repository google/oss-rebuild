// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"bytes"
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/attestation"
	"github.com/google/oss-rebuild/pkg/build"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/rebuild/stability"
	"github.com/google/oss-rebuild/pkg/stabilize"
	"github.com/google/uuid"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/grpc/codes"
)

type AnalyzerDeps struct {
	HTTPClient                 httpx.BasicClient
	Signer                     *dsse.EnvelopeSigner
	Verifier                   *dsse.EnvelopeVerifier
	GCBExecutor                *buildgcb.Executor
	ServiceRepo                rebuild.Location
	InputAttestationStore      rebuild.AssetStore
	OutputAnalysisStore        rebuild.LocatableAssetStore
	LocalMetadataStore         rebuild.LocatableAssetStore
	DebugStoreBuilder          func(ctx context.Context) (rebuild.LocatableAssetStore, error)
	RemoteMetadataStoreBuilder func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error)
	OverwriteAttestations      bool
}

func Analyze(ctx context.Context, req schema.AnalyzeRebuildRequest, deps *AnalyzerDeps) (*api.NoReturn, error) {
	t := rebuild.Target{
		Ecosystem: req.Ecosystem,
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	// Check if analysis already exists
	if !deps.OverwriteAttestations {
		if exists, err := analysisExists(ctx, deps.OutputAnalysisStore, t); err != nil {
			return nil, errors.Wrap(err, "checking existing analysis")
		} else if exists {
			return nil, api.AsStatus(codes.AlreadyExists, errors.New("analysis already exists"))
		}
	}
	return analyzeRebuild(ctx, t, deps)
}

func analysisExists(ctx context.Context, store rebuild.AssetStore, t rebuild.Target) (bool, error) {
	_, err := store.Reader(ctx, NetworkAnalysisAsset.For(t))
	if errors.Is(err, rebuild.ErrAssetNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func analyzeRebuild(ctx context.Context, t rebuild.Target, deps *AnalyzerDeps) (*api.NoReturn, error) {
	rebuildAttestation, err := getRebuildAttestation(ctx, deps.InputAttestationStore, t, deps.Verifier)
	if err != nil {
		return nil, errors.Wrap(err, "getting rebuild attestation")
	}
	strategy, err := getStrategy(rebuildAttestation)
	if err != nil {
		return nil, errors.Wrap(err, "extracting strategy from attestation")
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	mux := meta.NewRegistryMux(httpx.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{}))
	id, err := executeNetworkRebuild(ctx, deps, t, strategy, rebuildAttestation)
	if err != nil {
		return nil, errors.Wrap(err, "network rebuild failed")
	}
	err = createAndPublishAttestations(ctx, deps, mux, t, strategy, id, rebuildAttestation)
	if err != nil {
		return nil, errors.Wrap(err, "publishing attestations")
	}
	return &api.NoReturn{}, nil
}

// getRebuildAttestation fetches and parses the rebuild attestation from the store
func getRebuildAttestation(ctx context.Context, store rebuild.AssetStore, t rebuild.Target, verifier *dsse.EnvelopeVerifier) (*attestation.RebuildAttestation, error) {
	bundleReader, err := store.Reader(ctx, rebuild.AttestationBundleAsset.For(t))
	if err != nil {
		return nil, errors.Wrap(err, "reading attestation bundle")
	}
	defer bundleReader.Close()
	bundleData, err := io.ReadAll(bundleReader)
	if err != nil {
		return nil, errors.Wrap(err, "reading bundle data")
	}
	bundle, err := attestation.NewBundle(ctx, bundleData, verifier)
	if err != nil {
		return nil, errors.Wrap(err, "parsing bundle")
	}
	rebuildAttestation, err := attestation.FilterForOne[attestation.RebuildAttestation](
		bundle,
		attestation.WithBuildType(attestation.BuildTypeRebuildV01),
	)
	if err != nil {
		return nil, errors.New("expected exactly one rebuild attestation")
	}
	return rebuildAttestation, nil
}

func getStrategy(rebuildAttestation *attestation.RebuildAttestation) (rebuild.Strategy, error) {
	strategyBytes := rebuildAttestation.Predicate.RunDetails.Byproducts.BuildStrategy.Content
	var strategyOneOf schema.StrategyOneOf
	if err := json.Unmarshal(strategyBytes, &strategyOneOf); err != nil {
		return nil, errors.Wrap(err, "unmarshaling strategy")
	}
	return strategyOneOf.Strategy()
}

func getBuildDefinition(rebuildAttestation *attestation.RebuildAttestation) (*schema.BuildDefinition, error) {
	if rebuildAttestation.Predicate.BuildDefinition.ResolvedDependencies.BuildFix != nil {
		buildFixContent := rebuildAttestation.Predicate.BuildDefinition.ResolvedDependencies.BuildFix.Content
		var buildDef schema.BuildDefinition
		if err := json.Unmarshal(buildFixContent, &buildDef); err != nil {
			return nil, errors.Wrap(err, "unmarshaling build definition")
		}
		return &buildDef, nil
	}
	return nil, nil
}

func compareArtifacts(ctx context.Context, mux rebuild.RegistryMux, t rebuild.Target, remoteStore rebuild.LocatableAssetStore, rebuildAttestation *attestation.RebuildAttestation) (*verifier.ArtifactSummary, *verifier.ArtifactSummary, error) {
	rebuilder, ok := meta.AllRebuilders[t.Ecosystem]
	if !ok {
		return nil, nil, errors.New("unsupported ecosystem")
	}
	upstreamURI, err := rebuilder.UpstreamURL(ctx, t, mux)
	if err != nil {
		return nil, nil, errors.Wrap(err, "getting upstream URL")
	}
	stabilizers, err := stability.StabilizersForTarget(t)
	if err != nil {
		return nil, nil, errors.Wrap(err, "getting stabilizers for target")
	}
	if buildDef, err := getBuildDefinition(rebuildAttestation); err != nil {
		return nil, nil, errors.Wrap(err, "getting build definition from attestation")
	} else if buildDef != nil && len(buildDef.CustomStabilizers) > 0 {
		customStabilizers, err := stabilize.CreateCustomStabilizers(buildDef.CustomStabilizers, t.ArchiveType())
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating custom stabilizers")
		}
		stabilizers = append(stabilizers, customStabilizers...)
	}
	hashes := []crypto.Hash{crypto.SHA256}
	if t.Ecosystem == rebuild.NPM {
		hashes = append(hashes, crypto.SHA512)
	}
	rb, up, err := verifier.SummarizeArtifacts(ctx, remoteStore, t, upstreamURI, hashes, stabilizers)
	if err != nil {
		return nil, nil, errors.Wrap(err, "comparing artifacts")
	}
	// Compare summaries similar to buildAndAttest
	exactMatch := bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	stabilizedMatch := bytes.Equal(rb.StabilizedHash.Sum(nil), up.StabilizedHash.Sum(nil))
	if !exactMatch && !stabilizedMatch {
		return nil, nil, errors.New("rebuild content mismatch")
	}
	return &rb, &up, nil
}

func executeNetworkRebuild(ctx context.Context, deps *AnalyzerDeps, t rebuild.Target, strategy rebuild.Strategy, rebuildAttestation *attestation.RebuildAttestation) (string, error) {
	obID := uuid.New().String()
	debugStore, err := deps.DebugStoreBuilder(context.WithValue(ctx, rebuild.RunID, obID))
	if err != nil {
		return "", errors.Wrap(err, "creating debug store")
	}
	remoteMetadata, err := deps.RemoteMetadataStoreBuilder(ctx, obID)
	if err != nil {
		return "", errors.Wrap(err, "creating rebuild store")
	}
	buildStore := rebuild.NewMixedAssetStore(map[rebuild.AssetType]rebuild.LocatableAssetStore{
		rebuild.ContainerImageAsset: remoteMetadata,
		rebuild.RebuildAsset:        remoteMetadata,
		rebuild.ProxyNetlogAsset:    remoteMetadata,
		rebuild.DockerfileAsset:     deps.LocalMetadataStore,
		rebuild.BuildInfoAsset:      deps.LocalMetadataStore,
	})
	prebuildConfig := rebuildAttestation.Predicate.BuildDefinition.InternalParameters.PrebuildConfig
	toolURLs := map[build.ToolType]string{
		build.TimewarpTool: "gs://" + path.Join(prebuildConfig.Bucket, prebuildConfig.Dir, "timewarp"),
		build.GSUtilTool:   "gs://" + path.Join(prebuildConfig.Bucket, prebuildConfig.Dir, "gsutil_writeonly"),
	}
	var authRequired []string
	if prebuildConfig.Auth {
		authRequired = append(authRequired, "gs://"+prebuildConfig.Bucket)
	}
	in := rebuild.Input{
		Target:   t,
		Strategy: strategy,
	}
	h, err := deps.GCBExecutor.Start(ctx, in, build.Options{
		BuildID:         obID,
		UseTimewarp:     meta.AllRebuilders[t.Ecosystem].UsesTimewarp(in),
		UseNetworkProxy: true, // The whole point of the analyzer
		Resources: build.Resources{
			AssetStore:       buildStore,
			ToolURLs:         toolURLs,
			ToolAuthRequired: authRequired,
			BaseImageConfig:  build.DefaultBaseImageConfig(),
		},
	})
	if err != nil {
		return "", errors.Wrap(err, "starting build")
	}
	_, err = h.Wait(ctx)
	if err != nil {
		return "", errors.Wrap(err, "rebuild failed")
	}
	defer func() {
		for _, a := range []rebuild.AssetType{rebuild.DockerfileAsset, rebuild.BuildInfoAsset} {
			rebuild.AssetCopy(ctx, debugStore, deps.LocalMetadataStore, a.For(t))
		}
	}()
	return obID, nil
}

func createAndPublishAttestations(ctx context.Context, deps *AnalyzerDeps, mux rebuild.RegistryMux, t rebuild.Target, strategy rebuild.Strategy, obID string, rebuildAttestation *attestation.RebuildAttestation) error {
	remoteStore, err := deps.RemoteMetadataStoreBuilder(ctx, obID)
	if err != nil {
		return errors.Wrap(err, "creating remote store for comparison")
	}
	err = copyNetworkLog(ctx, remoteStore, deps.OutputAnalysisStore, t)
	if err != nil {
		return errors.Wrap(err, "copying network log")
	}
	// Perform artifact comparison to ensure a successful rebuild
	rebuiltSummary, upstreamSummary, err := compareArtifacts(ctx, mux, t, remoteStore, rebuildAttestation)
	if err != nil {
		return errors.Wrap(err, "comparing artifacts")
	}
	// Create network attestations
	networkStmt, eqStmt, err := createNetworkAttestations(ctx, t, strategy, obID, deps.ServiceRepo, deps.OutputAnalysisStore, deps.InputAttestationStore, deps.LocalMetadataStore, rebuildAttestation, *rebuiltSummary, *upstreamSummary)
	if err != nil {
		return errors.Wrap(err, "creating network attestations")
	}
	// Publish bundle with both attestations
	signer := verifier.InTotoEnvelopeSigner{EnvelopeSigner: deps.Signer}
	err = publishNetworkBundle(ctx, deps.OutputAnalysisStore, signer, t, eqStmt, networkStmt)
	if err != nil {
		return errors.Wrap(err, "publishing bundle")
	}

	return nil
}

func copyNetworkLog(ctx context.Context, remoteStore, analysisStore rebuild.LocatableAssetStore, t rebuild.Target) error {
	netlogReader, err := remoteStore.Reader(ctx, rebuild.ProxyNetlogAsset.For(t))
	if err != nil {
		return errors.Wrap(err, "reading network log from remote store")
	}
	defer netlogReader.Close()
	netlogWriter, err := analysisStore.Writer(ctx, NetworkLogAsset.For(t))
	if err != nil {
		return errors.Wrap(err, "creating network log writer")
	}
	defer netlogWriter.Close()
	_, err = io.Copy(netlogWriter, netlogReader)
	return err
}

func createNetworkAttestations(ctx context.Context, t rebuild.Target, strategy rebuild.Strategy, obID string, serviceLoc rebuild.Location, analysisStore rebuild.LocatableAssetStore, inputAttestationStore rebuild.AssetStore, metadataStore rebuild.AssetStore, rebuildAttestation *attestation.RebuildAttestation, rebuiltSummary, upstreamSummary verifier.ArtifactSummary) (network, equivalence *in_toto.ProvenanceStatementSLSA1, err error) {
	subjectDigest := rebuildAttestation.Subject[0].Digest
	// Calculate network log digest
	netlogReader, err := analysisStore.Reader(ctx, NetworkLogAsset.For(t))
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading network log for digest")
	}
	defer netlogReader.Close()
	hasher := hashext.NewTypedHash(crypto.SHA256)
	if _, err := io.Copy(hasher, netlogReader); err != nil {
		return nil, nil, errors.Wrap(err, "hashing network log")
	}
	netlogURL := analysisStore.URL(NetworkLogAsset.For(t))
	netlogDescriptor := slsa1.ResourceDescriptor{
		Name: netlogURL.String(),
		Digest: common.DigestSet{
			verifier.ToNISTName(hasher.Algorithm): hex.EncodeToString(hasher.Sum(nil)),
		},
	}
	strategyBytes, err := json.Marshal(schema.NewStrategyOneOf(strategy))
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshalling strategy")
	}
	var deps NetworkRebuildDeps
	bundleReader, err := inputAttestationStore.Reader(ctx, rebuild.AttestationBundleAsset.For(t))
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading attestation bundle for digest")
	}
	defer bundleReader.Close()
	bundleHasher := hashext.NewTypedHash(crypto.SHA256)
	if _, err := io.Copy(bundleHasher, bundleReader); err != nil {
		return nil, nil, errors.Wrap(err, "hashing attestation bundle")
	}
	deps.AttestationBundle = slsa1.ResourceDescriptor{
		Name: string(rebuild.AttestationBundleAsset),
		Digest: common.DigestSet{
			verifier.ToNISTName(bundleHasher.Algorithm): hex.EncodeToString(bundleHasher.Sum(nil)),
		},
	}
	if inst, err := strategy.GenerateFor(t, rebuild.BuildEnv{TimewarpHost: "example.internal"}); err == nil {
		if inst.Location.Ref != "" {
			deps.Source = &slsa1.ResourceDescriptor{
				Name:   "git+" + inst.Location.Repo,
				Digest: verifier.GitDigestSet(inst.Location),
			}
		}
	}
	var buildStepsBytes []byte
	{
		var bi rebuild.BuildInfo
		reader, err := metadataStore.Reader(ctx, rebuild.BuildInfoAsset.For(t))
		if err != nil {
			return nil, nil, errors.Wrap(err, "reading build info")
		}
		defer reader.Close()
		if err := json.NewDecoder(reader).Decode(&bi); err != nil {
			return nil, nil, errors.Wrap(err, "decoding build info")
		}
		buildStepsBytes, err = json.Marshal(bi.Steps)
		if err != nil {
			return nil, nil, errors.Wrap(err, "serializing build steps")
		}
	}
	internalParams := attestation.ServiceInternalParams{
		ServiceSource:  attestation.SourceLocationFromLocation(serviceLoc),
		PrebuildSource: rebuildAttestation.Predicate.BuildDefinition.InternalParameters.PrebuildSource,
		PrebuildConfig: rebuildAttestation.Predicate.BuildDefinition.InternalParameters.PrebuildConfig,
	}
	builder := slsa1.Builder{ID: attestation.HostGoogle}
	metadata := slsa1.BuildMetadata{InvocationID: obID}
	// Create network attestation
	network, err = (&NetworkRebuildAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: t.Artifact, Digest: subjectDigest}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: NetworkRebuildPredicate{
			BuildDefinition: NetworkRebuildBuildDef{
				BuildType: BuildTypeNetworkRebuildV01,
				ExternalParameters: NetworkRebuildParams{
					Ecosystem: string(t.Ecosystem),
					Package:   t.Package,
					Version:   t.Version,
					Artifact:  t.Artifact,
				},
				InternalParameters:   internalParams,
				ResolvedDependencies: deps,
			},
			RunDetails: NetworkRebuildRunDetails{
				Builder:       builder,
				BuildMetadata: metadata,
				Byproducts: NetworkRebuildByproducts{
					NetworkLog:    netlogDescriptor,
					BuildStrategy: slsa1.ResourceDescriptor{Name: attestation.ByproductBuildStrategy, Content: strategyBytes},
					BuildSteps:    slsa1.ResourceDescriptor{Name: attestation.ByproductBuildSteps, Content: buildStepsBytes},
				},
			},
		},
	}).ToStatement()
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating network attestation")
	}
	// Create equivalence attestation
	rebuiltDigests := make(common.DigestSet)
	for _, hash := range rebuiltSummary.Hash {
		rebuiltDigests[verifier.ToNISTName(hash.Algorithm)] = hex.EncodeToString(hash.Sum(nil))
	}
	upstreamDigests := make(common.DigestSet)
	for _, hash := range upstreamSummary.Hash {
		upstreamDigests[verifier.ToNISTName(hash.Algorithm)] = hex.EncodeToString(hash.Sum(nil))
	}
	publicRebuildURI := path.Join("rebuild", t.Artifact)
	publicStabilizedURI := path.Join("stabilized", t.Artifact)
	equivalence, err = (&attestation.ArtifactEquivalenceAttestation{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			Subject:       []in_toto.Subject{{Name: t.Artifact, Digest: subjectDigest}},
			PredicateType: slsa1.PredicateSLSAProvenance,
		},
		Predicate: attestation.ArtifactEquivalencePredicate{
			BuildDefinition: attestation.ArtifactEquivalenceBuildDef{
				BuildType: attestation.BuildTypeArtifactEquivalenceV01,
				ExternalParameters: attestation.ArtifactEquivalenceParams{
					Candidate: publicRebuildURI,
					Target:    publicStabilizedURI,
				},
				InternalParameters: internalParams,
				ResolvedDependencies: attestation.ArtifactEquivalenceDeps{
					RebuiltArtifact:  slsa1.ResourceDescriptor{Name: publicRebuildURI, Digest: rebuiltDigests},
					UpstreamArtifact: slsa1.ResourceDescriptor{Name: publicStabilizedURI, Digest: upstreamDigests},
				},
			},
			RunDetails: attestation.ArtifactEquivalenceRunDetails{
				Builder:       builder,
				BuildMetadata: metadata,
				Byproducts: attestation.ArtifactEquivalenceByproducts{
					StabilizedArtifact: slsa1.ResourceDescriptor{Name: publicRebuildURI, Digest: rebuiltDigests},
				},
			},
		},
	}).ToStatement()
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating equivalence attestation")
	}

	return network, equivalence, nil
}

func publishNetworkBundle(ctx context.Context, store rebuild.AssetStore, signer verifier.InTotoEnvelopeSigner, t rebuild.Target, statements ...*in_toto.ProvenanceStatementSLSA1) error {
	w, err := store.Writer(ctx, NetworkAnalysisAsset.For(t))
	if err != nil {
		return errors.Wrap(err, "creating writer")
	}
	defer w.Close()
	for _, stmt := range statements {
		envelope, err := signer.SignStatement(ctx, stmt)
		if err != nil {
			return errors.Wrap(err, "signing statement")
		}
		if err := json.NewEncoder(w).Encode(envelope); err != nil {
			return errors.Wrap(err, "writing envelope")
		}
	}
	return nil
}
