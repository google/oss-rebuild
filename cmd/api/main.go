// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/cache"
	gcb "github.com/google/oss-rebuild/internal/cloudbuild"
	httpinternal "github.com/google/oss-rebuild/internal/http"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/internal/verifier"
	"github.com/google/oss-rebuild/pkg/builddef"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
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
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
)

var (
	project               = flag.String("project", "", "GCP Project ID for storage and build resources")
	buildRemoteIdentity   = flag.String("build-remote-identity", "", "Identity from which to run remote rebuilds")
	buildLocalURL         = flag.String("build-local-url", "", "URL of the rebuild service")
	inferenceURL          = flag.String("inference-url", "", "URL of the inference service")
	signingKeyVersion     = flag.String("signing-key-version", "", "Resource name of the signing CryptoKeyVersion")
	metadataBucket        = flag.String("metadata-bucket", "", "GCS bucket for rebuild metadata")
	attestationBucket     = flag.String("attestation-bucket", "", "GCS bucket to which to publish rebuild attestation")
	logsBucket            = flag.String("logs-bucket", "", "GCS bucket for rebuild logs")
	prebuildBucket        = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	buildDefRepo          = flag.String("build-def-repo", "", "repository for build definitions")
	buildDefRepoDir       = flag.String("build-def-repo-dir", ".", "relpath within the build definitions repository")
	overwriteAttestations = flag.Bool("overwrite-attestations", false, "whether to overwrite existing attestations when writing to GCS")
)

var httpcfg = httpegress.Config{}

type RebuildSmoketestDeps struct {
	FirestoreClient *firestore.Client
	SmoketestStub   api.StubT[schema.SmoketestRequest, schema.SmoketestResponse]
	VersionStub     api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func RebuildSmoketestInit(ctx context.Context) (*RebuildSmoketestDeps, error) {
	var d RebuildSmoketestDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	u, err := url.Parse(*buildLocalURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing build local URL")
	}
	runclient, err := idtoken.NewClient(ctx, *buildLocalURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing build local client")
	}
	d.SmoketestStub = api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](runclient, *u.JoinPath("smoketest"))
	d.VersionStub = api.Stub[schema.VersionRequest, schema.VersionResponse](runclient, *u.JoinPath("version"))
	return &d, nil
}

func RebuildSmoketest(ctx context.Context, sreq schema.SmoketestRequest, deps *RebuildSmoketestDeps) (*schema.SmoketestResponse, error) {
	if sreq.ID == "" {
		sreq.ID = time.Now().UTC().Format(time.RFC3339)
	}
	log.Printf("Dispatching smoketest: %v", sreq)
	versionChan := make(chan string, 1)
	go func() {
		resp, err := deps.VersionStub(ctx, schema.VersionRequest{Service: "build-local"})
		if err != nil {
			log.Println(errors.Wrap(err, "making version request"))
			versionChan <- "unknown"
		} else {
			versionChan <- resp.Version
		}
		close(versionChan)
	}()
	resp, stuberr := deps.SmoketestStub(ctx, sreq)
	var verdicts []schema.Verdict
	var executor string
	if errors.Is(stuberr, api.ErrNotOK) {
		log.Printf("smoketest failed: %v\n", stuberr)
		// If smoketest failed, populate the verdicts with as much info as we can (pulling executor
		// version from the smoketest version endpoint if possible.
		executor = <-versionChan
		for _, v := range sreq.Versions {
			verdicts = append(verdicts, schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(sreq.Ecosystem),
					Package:   sreq.Package,
					Version:   v,
					// TODO: This should be populated by the sreq when we support different artifact types.
					Artifact: "",
				},
				Message: fmt.Sprintf("build-local failed: %v", stuberr),
			})
		}
	} else if stuberr != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(stuberr, "making smoketest request"))
	} else {
		verdicts = resp.Verdicts
		executor = resp.Executor
	}
	for _, v := range verdicts {
		var rawStrategy string
		if enc, err := json.Marshal(v.StrategyOneof); err != nil {
			log.Printf("invalid strategy returned from smoketest: %v\n", err)
		} else {
			rawStrategy = string(enc)
		}
		_, err := deps.FirestoreClient.Collection("ecosystem").Doc(string(v.Target.Ecosystem)).Collection("packages").Doc(sanitize(sreq.Package)).Collection("versions").Doc(v.Target.Version).Collection("attempts").Doc(sreq.ID).Set(ctx, schema.SmoketestAttempt{
			Ecosystem:         string(v.Target.Ecosystem),
			Package:           v.Target.Package,
			Version:           v.Target.Version,
			Artifact:          v.Target.Artifact,
			Success:           v.Message == "",
			Message:           v.Message,
			Strategy:          rawStrategy,
			TimeCloneEstimate: v.Timings.Source.Seconds(),
			TimeSource:        v.Timings.Source.Seconds(),
			TimeInfer:         v.Timings.Infer.Seconds(),
			TimeBuild:         v.Timings.Build.Seconds(),
			ExecutorVersion:   executor,
			RunID:             sreq.ID,
			Created:           time.Now().UnixMilli(),
		})
		if err != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "writing record for %s@%s", sreq.Package, v.Target.Version))
		}
	}
	if stuberr != nil {
		// TODO: Pass on status code here.
		return nil, api.AsStatus(codes.Internal, errors.Wrap(stuberr, "executing smoketest"))
	}
	return resp, nil
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

func makeKMSSigner(ctx context.Context, cryptoKeyVersion string) (*dsse.EnvelopeSigner, error) {
	kc, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	ckv, err := kc.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: cryptoKeyVersion})
	if err != nil {
		return nil, errors.Wrap(err, "fetching CryptoKeyVersion")
	}
	kmsSigner, err := kmsdsse.NewCloudKMSSigner(ctx, kc, ckv)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudKMSSigner")
	}
	dsseSigner, err := dsse.NewEnvelopeSigner(kmsSigner)
	if err != nil {
		return nil, errors.Wrap(err, "creating EnvelopeSigner")
	}
	return dsseSigner, nil
}

type RebuildPackageDeps struct {
	HTTPClient            httpinternal.BasicClient
	Signer                *dsse.EnvelopeSigner
	GCBClient             gcb.Client
	BuildDefRepo          rebuild.Location
	AttestationStore      rebuild.AssetStore
	MetadataBuilder       func(ctx context.Context, id string) (rebuild.AssetStore, error)
	OverwriteAttestations bool
	InferStub             api.StubT[schema.InferenceRequest, schema.StrategyOneOf]
}

func RebuildPackageInit(ctx context.Context) (*RebuildPackageDeps, error) {
	var d RebuildPackageDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	d.Signer, err = makeKMSSigner(ctx, *signingKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating signer")
	}
	svc, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudBuild service")
	}
	d.GCBClient = &gcb.Service{Service: svc}
	repo, err := uri.CanonicalizeRepoURI(*buildDefRepo)
	if err != nil {
		return nil, errors.Wrap(err, "canonicalizing build def repo")
	}
	d.BuildDefRepo = rebuild.Location{
		Repo: repo,
		Ref:  plumbing.Main.String(),
		Dir:  path.Clean(*buildDefRepoDir),
	}
	d.AttestationStore, err = rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*attestationBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating attestation uploader")
	}
	d.MetadataBuilder = func(ctx context.Context, id string) (rebuild.AssetStore, error) {
		return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, id), "gs://"+*metadataBucket)
	}
	d.OverwriteAttestations = *overwriteAttestations
	u, err := url.Parse(*inferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing inference URL")
	}
	u = u.JoinPath("infer")
	runclient, err := idtoken.NewClient(ctx, *inferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing inference client")
	}
	d.InferStub = api.Stub[schema.InferenceRequest, schema.StrategyOneOf](runclient, *u)
	return &d, nil
}

func RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest, deps *RebuildPackageDeps) (*api.NoReturn, error) {
	t := rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version}
	ctx = context.WithValue(ctx, rebuild.HTTPBasicClientID, deps.HTTPClient)
	regclient := httpinternal.NewCachedClient(deps.HTTPClient, &cache.CoalescingMemoryCache{})
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
		Project:             *project,
		BuildServiceAccount: *buildRemoteIdentity,
		UtilPrebuildBucket:  *prebuildBucket,
		LogsBucket:          *logsBucket,
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

func sanitize(key string) string {
	return strings.ReplaceAll(key, "/", "!")
}

func HandleGet(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	// FIXME encode scope in docref
	pkg, version := req.Form.Get("pkg"), req.Form.Get("version")
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		log.Println(err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	var snapshot *firestore.DocumentSnapshot
	iter := client.Collection("packages").Doc(sanitize(pkg)).Collection("versions").Doc(version).Collection("attempts").Limit(1).Documents(ctx)
	snapshot, err = iter.Next()
	if err == iterator.Done {
		http.Error(rw, "Not Found", 404)
		return
	}
	if err != nil {
		log.Println(err)
		http.Error(rw, "Internal Error", 500)
		return
	}
	ret, err := json.Marshal(map[string]any{
		"package":          snapshot.Data()["package"].(string),
		"version":          snapshot.Data()["version"].(string),
		"success":          snapshot.Data()["success"].(bool),
		"message":          snapshot.Data()["message"].(string),
		"executor_version": snapshot.Data()["executor_version"].(string),
		"created":          time.UnixMilli(snapshot.Data()["created"].(int64)),
	})
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Write(ret)
}

type VersionDeps struct {
	FirestoreClient       *firestore.Client
	BuildLocalVersionStub api.StubT[schema.VersionRequest, schema.VersionResponse]
	InferenceVersionStub  api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func VersionInit(ctx context.Context) (*VersionDeps, error) {
	var d VersionDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	{
		u, err := url.Parse(*buildLocalURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing build local URL")
		}
		runclient, err := idtoken.NewClient(ctx, *buildLocalURL)
		if err != nil {
			return nil, errors.Wrap(err, "initializing build local client")
		}
		d.BuildLocalVersionStub = api.Stub[schema.VersionRequest, schema.VersionResponse](runclient, *u.JoinPath("version"))
	}
	{
		u, err := url.Parse(*inferenceURL)
		if err != nil {
			return nil, errors.Wrap(err, "parsing inference URL")
		}
		runclient, err := idtoken.NewClient(ctx, *inferenceURL)
		if err != nil {
			return nil, errors.Wrap(err, "initializing inference client")
		}
		d.InferenceVersionStub = api.Stub[schema.VersionRequest, schema.VersionResponse](runclient, *u.JoinPath("version"))
	}
	return &d, nil
}

func Version(ctx context.Context, req schema.VersionRequest, deps *VersionDeps) (*schema.VersionResponse, error) {
	switch req.Service {
	case "":
		return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
	case "build-local":
		return deps.BuildLocalVersionStub(ctx, req)
	case "inference":
		return deps.InferenceVersionStub(ctx, req)
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unknown service"))
	}
}

type CreateRunDeps struct {
	FirestoreClient *firestore.Client
}

func CreateRunInit(ctx context.Context) (*CreateRunDeps, error) {
	var d CreateRunDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &d, nil
}

func CreateRun(ctx context.Context, req schema.CreateRunRequest, deps *CreateRunDeps) (*schema.CreateRunResponse, error) {
	id := time.Now().UTC().Format(time.RFC3339)
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		return t.Create(deps.FirestoreClient.Collection("runs").Doc(id), map[string]any{
			"benchmark_name": req.Name,
			"benchmark_hash": req.Hash,
			"run_type":       req.Type,
			"created":        time.Now().UTC().UnixMilli(),
		})
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "firestore write"))
	}
	return &schema.CreateRunResponse{ID: id}, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/smoketest", api.Handler(RebuildSmoketestInit, RebuildSmoketest))
	http.HandleFunc("/rebuild", api.Handler(RebuildPackageInit, RebuildPackage))
	http.HandleFunc("/get", HandleGet)
	http.HandleFunc("/version", api.Handler(VersionInit, Version))
	http.HandleFunc("/runs", api.Handler(CreateRunInit, CreateRun))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
