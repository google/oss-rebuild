package apiservice

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/builddef"
	cratesrb "github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	npmrb "github.com/google/oss-rebuild/pkg/rebuild/npm"
	pypirb "github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	cratesreg "github.com/google/oss-rebuild/pkg/registry/cratesio"
	npmreg "github.com/google/oss-rebuild/pkg/registry/npm"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/grpc/codes"
)

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
		t.Artifact = fmt.Sprintf("%s-%s.tgz", sanitize(t.Package), t.Version)
	case rebuild.CratesIO:
		t.Artifact = fmt.Sprintf("%s-%s.crate", sanitize(t.Package), t.Version)
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
	default:
		return errors.New("unknown ecosystem")
	}
	return nil
}

type RebuildPackageDeps struct {
	HTTPClient                 httpx.BasicClient
	FirestoreClient            *firestore.Client
	Signer                     *dsse.EnvelopeSigner
	GCBClient                  gcb.Client
	BuildProject               string
	BuildServiceAccount        string
	UtilPrebuildBucket         string
	BuildLogsBucket            string
	BuildDefRepo               rebuild.Location
	AttestationStore           rebuild.AssetStore
	LocalMetadataStore         rebuild.AssetStore
	RemoteMetadataStoreBuilder func(ctx context.Context, id string) (rebuild.AssetStore, error)
	OverwriteAttestations      bool
	InferStub                  api.StubT[schema.InferenceRequest, schema.StrategyOneOf]
}

type repoStrategy struct {
	// The strategy that was pulled from the build def repo.
	RepoStrategy rebuild.Strategy
	// Details about which build def repo this strategy was pulled from.
	BuildDefLoc rebuild.Location
}

// getStrategy determines which strategy we should execute. If a build def repo was used, that data will be included as repoStrategy.
func getStrategy(ctx context.Context, deps *RebuildPackageDeps, t rebuild.Target, fromRepo bool) (rebuild.Strategy, *repoStrategy, error) {
	var strat rebuild.Strategy
	var rstrat *repoStrategy
	ireq := schema.InferenceRequest{
		Ecosystem: t.Ecosystem,
		Package:   t.Package,
		Version:   t.Version,
	}
	if fromRepo {
		defs, err := builddef.NewBuildDefinitionSetFromGit(&builddef.GitBuildDefinitionSetOptions{
			CloneOptions: git.CloneOptions{
				URL:           deps.BuildDefRepo.Repo,
				ReferenceName: plumbing.ReferenceName(deps.BuildDefRepo.Ref),
				Depth:         1,
				NoCheckout:    true,
			},
			RelativePath: deps.BuildDefRepo.Dir,
			// TODO: Limit this further to only the target's path we want.
			SparseCheckoutDirs: []string{deps.BuildDefRepo.Dir},
		})
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating build definition repo reader")
		}
		pth, _ := defs.Path(ctx, t)
		rstrat = &repoStrategy{
			BuildDefLoc: rebuild.Location{
				Repo: deps.BuildDefRepo.Repo,
				Ref:  defs.Ref().String(),
				Dir:  pth,
			},
		}
		rstrat.RepoStrategy, err = defs.Get(ctx, t)
		if err != nil {
			return nil, nil, errors.Wrap(err, "accessing build definition")
		}
		if hint, ok := rstrat.RepoStrategy.(*rebuild.LocationHint); ok && hint != nil {
			ireq.StrategyHint = &schema.StrategyOneOf{LocationHint: hint}
		} else if rstrat.RepoStrategy != nil {
			strat = rstrat.RepoStrategy
		}
	}
	if strat == nil {
		s, err := deps.InferStub(ctx, ireq)
		if err != nil {
			// TODO: Surface better error than Internal.
			return nil, nil, errors.Wrap(err, "fetching inference")
		}
		strat, err = s.Strategy()
		if err != nil {
			return nil, nil, errors.Wrap(err, "reading strategy")
		}
	}
	return strat, rstrat, nil
}

func buildAndAttest(ctx context.Context, deps *RebuildPackageDeps, mux rebuild.RegistryMux, a verifier.Attestor, t rebuild.Target, strat rebuild.Strategy, rstrat *repoStrategy) (err error) {
	id := uuid.New().String()
	remoteMetadata, err := deps.RemoteMetadataStoreBuilder(ctx, id)
	if err != nil {
		return errors.Wrap(err, "creating rebuild store")
	}
	hashes := []crypto.Hash{crypto.SHA256}
	opts := rebuild.RemoteOptions{
		GCBClient:           deps.GCBClient,
		Project:             deps.BuildProject,
		BuildServiceAccount: deps.BuildServiceAccount,
		UtilPrebuildBucket:  deps.UtilPrebuildBucket,
		LogsBucket:          deps.BuildLogsBucket,
		LocalMetadataStore:  deps.LocalMetadataStore,
		RemoteMetadataStore: remoteMetadata,
	}
	var upstreamURI string
	switch t.Ecosystem {
	case rebuild.NPM:
		hashes = append(hashes, crypto.SHA512)
		upstreamURI, err = doNPMRebuild(ctx, t, id, mux, strat, opts)
	case rebuild.CratesIO:
		upstreamURI, err = doCratesRebuild(ctx, t, id, mux, strat, opts)
	case rebuild.PyPI:
		upstreamURI, err = doPyPIRebuild(ctx, t, id, mux, strat, opts)
	default:
		return api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	if err != nil {
		return errors.Wrap(err, "rebuilding")
	}
	rb, up, err := verifier.SummarizeArtifacts(ctx, remoteMetadata, t, upstreamURI, hashes)
	if err != nil {
		return errors.Wrap(err, "comparing artifacts")
	}
	exactMatch := bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	canonicalizedMatch := bytes.Equal(rb.CanonicalHash.Sum(nil), up.CanonicalHash.Sum(nil))
	if !exactMatch && !canonicalizedMatch {
		return api.AsStatus(codes.FailedPrecondition, errors.Wrap(err, "rebuild content mismatch"))
	}
	input := rebuild.Input{Target: t}
	var loc rebuild.Location
	if rstrat != nil {
		input.Strategy = rstrat.RepoStrategy
		loc = rstrat.BuildDefLoc
	}
	eqStmt, buildStmt, err := verifier.CreateAttestations(ctx, input, strat, id, rb, up, deps.LocalMetadataStore, loc)
	if err != nil {
		return errors.Wrap(err, "creating attestations")
	}
	if err := a.PublishBundle(ctx, t, eqStmt, buildStmt); err != nil {
		return errors.Wrap(err, "publishing bundle")
	}
	return nil
}

func rebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*schema.Verdict, error) {
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version}
	v := schema.Verdict{
		Target: t,
	}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	regclient := httpx.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{})
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: regclient},
		NPM:      npmreg.HTTPRegistry{Client: regclient},
		PyPI:     pypireg.HTTPRegistry{Client: regclient},
	}
	if err := populateArtifact(ctx, &t, mux); err != nil {
		// If we fail to populate artifact, the verdict has an incomplete target, which might prevent the storage of the verdict.
		// For this reason, we don't return a nil error and expect no verdict to be written.
		return nil, api.AsStatus(codes.InvalidArgument, errors.Wrap(err, "selecting artifact"))
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
	strat, rstrat, err := getStrategy(ctx, deps, t, req.StrategyFromRepo)
	if err != nil {
		v.Message = errors.Wrap(err, "getting strategy").Error()
		return &v, nil
	}
	if strat != nil {
		v.StrategyOneof = schema.NewStrategyOneOf(strat)
	}
	err = buildAndAttest(ctx, deps, mux, a, t, strat, rstrat)
	if err != nil {
		v.Message = errors.Wrap(err, "executing rebuild").Error()
		return &v, nil
	}
	return &v, nil
}

func RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*schema.Verdict, error) {
	v, err := rebuildPackage(ctx, req, deps)
	if err != nil {
		return nil, err
	}
	_, err = deps.FirestoreClient.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(v.Target.Package)).Collection("versions").Doc(v.Target.Version).Collection("attempts").Doc(req.ID).Set(ctx, schema.SmoketestAttempt{
		Ecosystem:       string(v.Target.Ecosystem),
		Package:         v.Target.Package,
		Version:         v.Target.Version,
		Artifact:        v.Target.Artifact,
		Success:         v.Message == "",
		Message:         v.Message,
		Strategy:        v.StrategyOneof,
		ExecutorVersion: os.Getenv("K_REVISION"),
		RunID:           req.ID,
		Created:         time.Now().UnixMilli(),
	})
	if err != nil {
		log.Print(errors.Wrap(err, "storing results in firestore"))
	}
	return v, nil
}
