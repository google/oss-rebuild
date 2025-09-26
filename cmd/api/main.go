// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"path"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/api/rebuilderservice"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/serviceid"
	"github.com/google/oss-rebuild/internal/uri"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/run/v2"
)

var (
	project               = flag.String("project", "", "GCP Project ID for storage and build resources")
	buildRemoteIdentity   = flag.String("build-remote-identity", "", "Identity from which to run remote rebuilds")
	buildLocalURL         = flag.String("build-local-url", "", "URL of the rebuild service")
	inferenceURL          = flag.String("inference-url", "", "URL of the inference service")
	signingKeyVersion     = flag.String("signing-key-version", "", "Resource name of the signing CryptoKeyVersion")
	metadataBucket        = flag.String("metadata-bucket", "", "GCS bucket for rebuild artifacts")
	attestationBucket     = flag.String("attestation-bucket", "", "GCS bucket to which to publish rebuild attestation")
	logsBucket            = flag.String("logs-bucket", "", "GCS bucket for rebuild logs")
	debugStorage          = flag.String("debug-storage", "", "if provided, the location in which rebuild debug info should be stored")
	prebuildBucket        = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	prebuildVersion       = flag.String("prebuild-version", "", "golang version identifier of the prebuild binary builds")
	prebuildAuth          = flag.Bool("prebuild-auth", false, "whether to authenticate requests to the prebuild tools bucket")
	buildDefRepo          = flag.String("build-def-repo", "", "repository for build definitions")
	buildDefRepoDir       = flag.String("build-def-repo-dir", ".", "relpath within the build definitions repository")
	overwriteAttestations = flag.Bool("overwrite-attestations", false, "whether to overwrite existing attestations when writing to GCS")
	blockLocalRepoPublish = flag.Bool("block-local-repo-publish", true, "whether to prevent attestation publishing when the BuildRepo property points to a file:// URI")
	gcbPrivatePoolName    = flag.String("gcb-private-pool-name", "", "Resoure name of GCB private pool to use, if configured")
	gcbPrivatePoolRegion  = flag.String("gcb-private-pool-region", "", "GCP location to use for GCB private pool builds, if configured. Note: This should generally be the same as the region where the private pool is located.")
	agentJobName          = flag.String("agent-job-name", "", "Name of the pre-created Cloud Run Job for AI agent")
	agentAPIURL           = flag.String("agent-api-url", "", "URL of the agent API service")
	agentSessionsBucket   = flag.String("agent-sessions-bucket", "", "GCS bucket for agent session data")
	agentMetadataBucket   = flag.String("agent-metadata-bucket", "", "GCS bucket for agent build metadata")
	agentLogsBucket       = flag.String("agent-logs-bucket", "", "GCS bucket for agent build logs")
	agentTimeoutSeconds   = flag.Int("agent-timeout-seconds", 3600, "Seconds to allow agent to run")
)

// Link-time configured service identity
var (
	// Repo from which the service was built
	BuildRepo string
	// Golang version identifier of the service container builds
	BuildVersion string
)

var httpcfg = httpegress.Config{}

func RebuildSmoketestInit(ctx context.Context) (*apiservice.RebuildSmoketestDeps, error) {
	var d apiservice.RebuildSmoketestDeps
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
	d.SmoketestStub = api.StubFromHandler(runclient, u.JoinPath("smoketest"), rebuilderservice.RebuildSmoketest)
	d.VersionStub = api.StubFromHandler(runclient, u.JoinPath("version"), rebuilderservice.Version)
	return &d, nil
}

func makeKMSSigner(ctx context.Context, cryptoKeyVersion string) (*dsse.EnvelopeSigner, error) {
	kc, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	kmsSigner, err := kmsdsse.NewCloudKMSSignerVerifier(ctx, kc, cryptoKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud KMS signer")
	}
	dsseSigner, err := dsse.NewEnvelopeSigner(kmsSigner)
	if err != nil {
		return nil, errors.Wrap(err, "creating envelope signer")
	}
	return dsseSigner, nil
}

func RebuildPackageInit(ctx context.Context) (*apiservice.RebuildPackageDeps, error) {
	var d apiservice.RebuildPackageDeps
	var err error
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	d.Signer, err = makeKMSSigner(ctx, *signingKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating signer")
	}
	svc, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudBuild service")
	}
	executorConfig := buildgcb.ExecutorConfig{
		Project:        *project,
		ServiceAccount: *buildRemoteIdentity,
		LogsBucket:     *logsBucket,
		Client:         nil, // Defined depending on gcbPrivatePoolName
	}
	if *gcbPrivatePoolName != "" {
		pool := &gcb.PrivatePoolConfig{
			Name:   *gcbPrivatePoolName,
			Region: *gcbPrivatePoolRegion,
		}
		executorConfig.PrivatePool = pool
		executorConfig.Client = gcb.NewClientWithPrivatePool(svc, pool)
	} else {
		executorConfig.Client = gcb.NewClient(svc)
	}
	d.GCBExecutor, err = buildgcb.NewExecutor(executorConfig)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCB executor")
	}
	d.ServiceRepo, err = serviceid.ParseLocation(BuildRepo, BuildVersion)
	if err != nil {
		return nil, errors.Wrap(err, "parsing service location")
	}
	d.PublishForLocalServiceRepo = !*blockLocalRepoPublish
	// TODO: Should we require/support a separate repo here?
	d.PrebuildRepo, err = serviceid.ParseLocation(BuildRepo, *prebuildVersion)
	if err != nil {
		return nil, errors.Wrap(err, "parsing prebuild location")
	}
	d.PrebuildConfig.Bucket = *prebuildBucket
	d.PrebuildConfig.Auth = *prebuildAuth
	// NOTE: The subdir will match the version identifier used for the service version.
	d.PrebuildConfig.Dir = d.PrebuildRepo.Ref
	buildDefRepo, err := uri.CanonicalizeRepoURI(*buildDefRepo)
	if err != nil {
		return nil, errors.Wrap(err, "canonicalizing build def repo")
	}
	d.BuildDefRepo = rebuild.Location{
		Repo: buildDefRepo,
		Ref:  plumbing.Main.String(),
		Dir:  path.Clean(*buildDefRepoDir),
	}
	d.AttestationStore, err = rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*attestationBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating attestation uploader")
	}
	d.LocalMetadataStore = rebuild.NewFilesystemAssetStore(memfs.New())
	// TODO: This can be optional once LocalMetadata and DebugStore are combined into a cached store.
	if *debugStorage == "" {
		return nil, errors.New("debug-storage must be set")
	}
	d.DebugStoreBuilder = func(ctx context.Context) (rebuild.AssetStore, error) {
		if ctx.Value(rebuild.RunID) == nil {
			return nil, errors.New("RunID must be set in the context")
		}
		return rebuild.DebugStoreFromContext(context.WithValue(ctx, rebuild.DebugStoreID, *debugStorage))
	}
	d.RemoteMetadataStoreBuilder = func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error) {
		return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, uuid), "gs://"+*metadataBucket)
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
	d.InferStub = api.StubFromHandler(runclient, u, inferenceservice.Infer)
	return &d, nil
}

func VersionInit(ctx context.Context) (*apiservice.VersionDeps, error) {
	var d apiservice.VersionDeps
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
		d.BuildLocalVersionStub = api.StubFromHandler(runclient, u.JoinPath("version"), rebuilderservice.Version)
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
		d.InferenceVersionStub = api.StubFromHandler(runclient, u.JoinPath("version"), inferenceservice.Version)
	}
	return &d, nil
}

func CreateRunInit(ctx context.Context) (*apiservice.CreateRunDeps, error) {
	var d apiservice.CreateRunDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &d, nil
}

func AgentCreateInit(ctx context.Context) (*apiservice.AgentCreateDeps, error) {
	var d apiservice.AgentCreateDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	d.RunService, err = run.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud Run service")
	}
	d.Project = *project
	d.AgentJobName = *agentJobName
	d.AgentAPIURL = *agentAPIURL
	d.AgentTimeoutSeconds = *agentTimeoutSeconds
	d.SessionsBucket = *agentSessionsBucket
	d.MetadataBucket = *agentMetadataBucket
	d.LogsBucket = *agentLogsBucket
	return &d, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/smoketest", api.Handler(RebuildSmoketestInit, apiservice.RebuildSmoketest))
	http.HandleFunc("/rebuild", api.Handler(RebuildPackageInit, apiservice.RebuildPackage))
	http.HandleFunc("/version", api.Handler(VersionInit, apiservice.Version))
	http.HandleFunc("/runs", api.Handler(CreateRunInit, apiservice.CreateRun))
	http.HandleFunc("/agent", api.Handler(AgentCreateInit, apiservice.AgentCreate))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
