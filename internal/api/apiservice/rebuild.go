package apiservice

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	gcb "github.com/google/oss-rebuild/internal/cloudbuild"
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
	HTTPClient            httpx.BasicClient
	Signer                *dsse.EnvelopeSigner
	GCBClient             gcb.Client
	BuildProject          string
	BuildServiceAccount   string
	UtilPrebuildBucket    string
	BuildLogsBucket       string
	BuildDefRepo          rebuild.Location
	AttestationStore      rebuild.AssetStore
	MetadataBuilder       func(ctx context.Context, id string) (rebuild.AssetStore, error)
	OverwriteAttestations bool
	InferStub             api.StubT[schema.InferenceRequest, schema.StrategyOneOf]
}

func RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*api.NoReturn, error) {
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	regclient := httpx.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{})
	mux := rebuild.RegistryMux{
		CratesIO: cratesreg.HTTPRegistry{Client: regclient},
		NPM:      npmreg.HTTPRegistry{Client: regclient},
		PyPI:     pypireg.HTTPRegistry{Client: regclient},
	}
	if err := populateArtifact(ctx, &t, mux); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "selecting artifact"))
	}
	signer := verifier.InTotoEnvelopeSigner{EnvelopeSigner: deps.Signer}
	a := verifier.Attestor{Store: deps.AttestationStore, Signer: signer, AllowOverwrite: deps.OverwriteAttestations}
	if !deps.OverwriteAttestations {
		if exists, err := a.BundleExists(ctx, t); err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "checking existing bundle"))
		} else if exists {
			return nil, api.AsStatus(codes.AlreadyExists, errors.New("conflict with existing attestation bundle"))
		}
	}
	var manualStrategy, strategy rebuild.Strategy
	var buildDefLoc rebuild.Location
	ireq := schema.InferenceRequest{
		Ecosystem: req.Ecosystem,
		Package:   req.Package,
		Version:   req.Version,
	}
	if req.StrategyFromRepo {
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
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating build definition repo reader"))
		}
		pth, _ := defs.Path(ctx, t)
		buildDefLoc = rebuild.Location{
			Repo: deps.BuildDefRepo.Repo,
			Ref:  defs.Ref().String(),
			Dir:  pth,
		}
		manualStrategy, err := defs.Get(ctx, t)
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "accessing build definition"))
		}
		if hint, ok := manualStrategy.(*rebuild.LocationHint); ok && hint != nil {
			ireq.StrategyHint = &schema.StrategyOneOf{LocationHint: hint}
		} else if manualStrategy != nil {
			strategy = manualStrategy
		}
	}
	if strategy == nil {
		s, err := deps.InferStub(ctx, ireq)
		if err != nil {
			// TODO: Surface better error than Internal.
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "fetching inference"))
		}
		strategy, err = s.Strategy()
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "reading strategy"))
		}
	}
	id := uuid.New().String()
	metadata, err := deps.MetadataBuilder(ctx, id)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating metadata store"))
	}
	var upstreamURI string
	hashes := []crypto.Hash{crypto.SHA256}
	opts := rebuild.RemoteOptions{
		GCBClient:           deps.GCBClient,
		Project:             deps.BuildProject,
		BuildServiceAccount: deps.BuildServiceAccount,
		UtilPrebuildBucket:  deps.UtilPrebuildBucket,
		LogsBucket:          deps.BuildLogsBucket,
		MetadataStore:       metadata,
	}
	// TODO: These doRebuild functions should return a verdict, and this handler
	// should forward those to the caller as a schema.Verdict.
	switch t.Ecosystem {
	case rebuild.NPM:
		hashes = append(hashes, crypto.SHA512)
		upstreamURI, err = doNPMRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.CratesIO:
		upstreamURI, err = doCratesRebuild(ctx, t, id, mux, strategy, opts)
	case rebuild.PyPI:
		upstreamURI, err = doPyPIRebuild(ctx, t, id, mux, strategy, opts)
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unsupported ecosystem"))
	}
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "rebuilding"))
	}
	rb, up, err := verifier.SummarizeArtifacts(ctx, metadata, t, upstreamURI, hashes)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "comparing artifacts"))
	}
	exactMatch := bytes.Equal(rb.Hash.Sum(nil), up.Hash.Sum(nil))
	canonicalizedMatch := bytes.Equal(rb.CanonicalHash.Sum(nil), up.CanonicalHash.Sum(nil))
	if !exactMatch && !canonicalizedMatch {
		return nil, api.AsStatus(codes.FailedPrecondition, errors.Wrap(err, "rebuild content mismatch"))
	}
	input := rebuild.Input{Target: t, Strategy: manualStrategy}
	eqStmt, buildStmt, err := verifier.CreateAttestations(ctx, input, strategy, id, rb, up, metadata, buildDefLoc)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "creating attestations"))
	}
	if err := a.PublishBundle(ctx, t, eqStmt, buildStmt); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "publishing bundle"))
	}
	return nil, nil
}
