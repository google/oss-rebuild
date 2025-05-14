// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/analyzer/network/analyzerservice"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/gcb"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/serviceid"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"google.golang.org/api/cloudbuild/v1"
)

var (
	analysisBucket        = flag.String("analysis-bucket", "", "the GCS bucket to write out analysis and attestations")
	project               = flag.String("project", "", "Google Cloud project ID")
	buildServiceAccount   = flag.String("build-remote-identity", "", "service account name for remote builds")
	logsBucket            = flag.String("logs-bucket", "", "GCS bucket for build logs")
	metadataBucket        = flag.String("metadata-bucket", "", "GCS bucket for build metadata")
	attestationBucket     = flag.String("attestation-bucket", "", "GCS bucket for attestations")
	debugStorage          = flag.String("debug-storage", "", "storage path for debug artifacts")
	signingKeyVersion     = flag.String("signing-key-version", "", "KMS crypto key version for signing")
	verifyingKeyVersion   = flag.String("verifying-key-version", "", "KMS crypto key version for verification")
	overwriteAttestations = flag.Bool("overwrite-attestations", false, "whether to overwrite existing attestations")
)

// Link-time configured service identity
var (
	BuildRepo    string
	BuildVersion string
)

var httpcfg = httpegress.Config{}

func AnalyzerInit(ctx context.Context) (*analyzerservice.AnalyzerDeps, error) {
	if *debugStorage == "" {
		return nil, errors.New("debug-storage must be set")
	}
	// HTTP client for external requests
	httpClient, err := httpegress.MakeClient(ctx, httpcfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating HTTP client")
	}
	// KMS client and keys
	kmsClient, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	// Signing key
	signingKey, err := kmsClient.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{
		Name: *signingKeyVersion,
	})
	if err != nil {
		return nil, errors.Wrap(err, "getting signing key")
	}
	signerVerifier, err := kmsdsse.NewCloudKMSSignerVerifier(ctx, kmsClient, signingKey)
	if err != nil {
		return nil, errors.Wrap(err, "creating signer/verifier")
	}
	signer, err := dsse.NewEnvelopeSigner(signerVerifier)
	if err != nil {
		return nil, errors.Wrap(err, "creating envelope signer")
	}
	// Verification key (if different from signing key)
	var verifier *dsse.EnvelopeVerifier
	if *verifyingKeyVersion != "" {
		verifyingKey, err := kmsClient.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{
			Name: *verifyingKeyVersion,
		})
		if err != nil {
			return nil, errors.Wrap(err, "getting verifying key")
		}
		verifyingSignerVerifier, err := kmsdsse.NewCloudKMSSignerVerifier(ctx, kmsClient, verifyingKey)
		if err != nil {
			return nil, errors.Wrap(err, "creating verifying signer/verifier")
		}
		verifier, err = dsse.NewEnvelopeVerifier(verifyingSignerVerifier)
		if err != nil {
			return nil, errors.Wrap(err, "creating envelope verifier")
		}
	} else {
		// Use same key for verification
		verifier, err = dsse.NewEnvelopeVerifier(signerVerifier)
		if err != nil {
			return nil, errors.Wrap(err, "creating envelope verifier")
		}
	}
	// Cloud Build client
	cloudbuildService, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud Build service")
	}
	gcbClient := gcb.NewClient(cloudbuildService)
	// Storage setup
	inputAttestationStore, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*attestationBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating input attestation store")
	}
	outputAnalysisStore, err := rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, ""), "gs://"+*analysisBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating output analysis store")
	}
	localMetadataStore := rebuild.NewFilesystemAssetStore(memfs.New())
	// Debug store builder
	debugStoreBuilder := func(ctx context.Context) (rebuild.AssetStore, error) {
		if ctx.Value(rebuild.RunID) == nil {
			return nil, errors.New("RunID must be set in the context")
		}
		return rebuild.DebugStoreFromContext(context.WithValue(ctx, rebuild.DebugStoreID, *debugStorage))
	}
	remoteMetadataStoreBuilder := func(ctx context.Context, uuid string) (rebuild.LocatableAssetStore, error) {
		return rebuild.NewGCSStore(context.WithValue(ctx, rebuild.RunID, uuid), "gs://"+*metadataBucket)
	}
	serviceLoc, err := serviceid.ParseLocation(BuildRepo, BuildVersion)
	if err != nil {
		return nil, errors.Wrap(err, "parsing service location")
	}
	return &analyzerservice.AnalyzerDeps{
		HTTPClient:                 httpClient,
		Signer:                     signer,
		Verifier:                   verifier,
		GCBClient:                  gcbClient,
		BuildProject:               *project,
		BuildServiceAccount:        *buildServiceAccount,
		BuildLogsBucket:            *logsBucket,
		ServiceRepo:                serviceLoc,
		InputAttestationStore:      inputAttestationStore,
		OutputAnalysisStore:        outputAnalysisStore,
		LocalMetadataStore:         localMetadataStore,
		DebugStoreBuilder:          debugStoreBuilder,
		RemoteMetadataStoreBuilder: remoteMetadataStoreBuilder,
		OverwriteAttestations:      *overwriteAttestations,
	}, nil
}

func main() {
	httpcfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	http.HandleFunc("/analyze", api.Handler(AnalyzerInit, analyzerservice.Analyze))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
