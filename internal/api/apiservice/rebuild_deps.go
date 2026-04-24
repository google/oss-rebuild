// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"net/url"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/storage"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpegress"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/idtoken"
)

// MakeRebuildPackageDeps creates RebuildPackageDeps from a RebuildDepsConfig.
func MakeRebuildPackageDeps(ctx context.Context, cfg *schema.RebuildDepsConfig) (*RebuildPackageDeps, error) {
	var d RebuildPackageDeps
	var err error
	// Use default http config for now, or we could add it to RebuildDepsConfig
	d.HTTPClient, err = httpegress.MakeClient(ctx, httpegress.Config{})
	if err != nil {
		return nil, errors.Wrap(err, "making http client")
	}
	firestoreClient, err := firestore.NewClient(ctx, cfg.FirestoreProject)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	d.Attempts = db.NewFirestoreAttempts(firestoreClient)
	kc, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	kmsSignerVerifier, err := kmsdsse.NewCloudKMSSignerVerifier(ctx, kc, cfg.SigningKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud KMS signer/verifier")
	}
	d.Signer, err = dsse.NewEnvelopeSigner(kmsSignerVerifier)
	if err != nil {
		return nil, errors.Wrap(err, "creating envelope signer")
	}
	legacyVerifier := kmsdsse.NewLegacyKeyIDVerifier(kmsSignerVerifier)
	d.Verifier, err = dsse.NewEnvelopeVerifier(kmsSignerVerifier, legacyVerifier)
	if err != nil {
		return nil, errors.Wrap(err, "creating envelope verifier")
	}
	d.PublishForLocalServiceRepo = cfg.PublishForLocalServiceRepo
	d.OverwriteAttestations = cfg.OverwriteAttestations
	svc, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudBuild service")
	}
	plannerConfig := buildgcb.PlannerConfig{
		ServiceAccount:  cfg.BuildRemoteIdentity,
		AllowPrivileged: true,
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCS client")
	}
	executorConfig := buildgcb.ExecutorConfig{
		Project:        cfg.BuildProject,
		ServiceAccount: cfg.BuildRemoteIdentity,
		LogsBucket:     cfg.LogsBucket,
		LogsClientFunc: buildgcb.GCSLogsClient(gcsClient),
		Client:         nil, // Defined depending on GCBPrivatePoolName
	}
	if cfg.GCBPrivatePoolName != "" {
		executorConfig.Client = gcb.NewRegionalClient(svc, cfg.GCBPrivatePoolRegion)
		executorConfig.PrivatePool = &gcb.PrivatePoolConfig{
			Name:   cfg.GCBPrivatePoolName,
			Region: cfg.GCBPrivatePoolRegion,
		}
		executorConfig.JumboPool = &gcb.PrivatePoolConfig{
			Name:   cfg.GCBPrivatePoolName + "-jumbo",
			Region: cfg.GCBPrivatePoolRegion,
		}
	} else {
		executorConfig.Client = gcb.NewClient(svc)
	}
	executorConfig.Planner = buildgcb.NewPlanner(plannerConfig)
	d.GCBExecutor, err = buildgcb.NewExecutor(executorConfig)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCB executor")
	}
	d.PrebuildConfig = rebuild.PrebuildConfig{
		Bucket: cfg.PrebuildBucket,
		Dir:    cfg.PrebuildRef, // Assuming PrebuildRef is used as Dir
		Auth:   cfg.PrebuildAuth,
	}
	d.PrebuildRepo = rebuild.Location{
		Repo: cfg.PrebuildRepo,
		Ref:  cfg.PrebuildRef,
	}
	d.BuildDefRepo = rebuild.Location{
		Repo: cfg.BuildDefRepo,
		Ref:  cfg.BuildDefRef,
		Dir:  cfg.BuildDefDir,
	}
	d.PublishForLocalServiceRepo = false // Should probably be configurable
	d.AttestationStore, err = rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+cfg.AttestationBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating attestation uploader")
	}
	d.LocalMetadataStore = rebuild.NewFilesystemAssetStore(memfs.New())
	d.DebugStoreBuilder = func(ctx context.Context) (rebuild.AssetStore, error) {
		if ctx.Value(rebuild.RunID) == nil {
			return nil, errors.New("RunID must be set in the context")
		}
		return rebuild.DebugStoreFromContext(context.WithValue(ctx, rebuild.DebugStoreID, cfg.DebugStorage))
	}
	d.RemoteMetadataStoreBuilder = func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error) {
		return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, uuid), "gs://"+cfg.AssetBucket)
	}
	u, err := url.Parse(cfg.InferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing inference URL")
	}
	u = u.JoinPath("infer")
	runclient, err := idtoken.NewClient(ctx, cfg.InferenceURL)
	if err != nil {
		return nil, errors.Wrap(err, "initializing inference client")
	}
	d.InferStub = api.StubFromHandler(runclient, u, inferenceservice.Infer)
	return &d, nil
}
