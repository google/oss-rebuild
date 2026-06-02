// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/agentapiservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/serviceid"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
)

var (
	project              = flag.String("project", "", "GCP Project ID for storage and build resources")
	buildRemoteIdentity  = flag.String("build-remote-identity", "", "Identity from which to run remote rebuilds")
	logsBucket           = flag.String("logs-bucket", "", "GCS bucket for rebuild logs")
	metadataBucket       = flag.String("metadata-bucket", "", "GCS bucket for rebuild artifacts")
	gcbPrivatePoolName   = flag.String("gcb-private-pool-name", "", "Resource name of GCB private pool to use, if configured")
	gcbPrivatePoolRegion = flag.String("gcb-private-pool-region", "", "GCP location to use for GCB private pool builds, if configured")
	prebuildBucket       = flag.String("prebuild-bucket", "", "GCS bucket from which prebuilt build tools are stored")
	prebuildVersion      = flag.String("prebuild-version", "", "golang version identifier of the prebuild binary builds")
	prebuildAuth         = flag.Bool("prebuild-auth", false, "whether to authenticate requests to the prebuild tools bucket")
	port                 = flag.Int("port", 8080, "port on which to serve")

	// Scratch flags. Gated by --scratch-enabled.
	scratchEnabled      = flag.Bool("scratch-enabled", false, "register the scratch routes")
	scratchZone         = flag.String("scratch-zone", "", "GCE zone for scratch VMs (required when scratch enabled)")
	scratchStandardTmpl = flag.String("scratch-instance-standard-template", "", "GCE instance template URL for the standard machine class (required when scratch enabled)")
	scratchJumboTmpl    = flag.String("scratch-instance-jumbo-template", "", "GCE instance template URL for the jumbo machine class (optional)")
)

// Link-time configured service identity
var (
	BuildRepo    string
	BuildVersion string
)

func AgentCreateIterationInit(ctx context.Context) (*agentapiservice.AgentCreateIterationDeps, error) {
	var d agentapiservice.AgentCreateIterationDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCS client")
	}
	svc, err := cloudbuild.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating CloudBuild service")
	}
	var gcbClient gcb.Client
	var pool *gcb.PrivatePoolConfig
	if *gcbPrivatePoolName != "" {
		pool = &gcb.PrivatePoolConfig{
			Name:   *gcbPrivatePoolName,
			Region: *gcbPrivatePoolRegion,
		}
		gcbClient = gcb.NewRegionalClient(svc, *gcbPrivatePoolRegion)
	} else {
		gcbClient = gcb.NewClient(svc)
	}
	executorConfig := buildgcb.ExecutorConfig{
		Project:            *project,
		ServiceAccount:     *buildRemoteIdentity,
		LogsBucket:         *logsBucket,
		LogsClientFunc:     buildgcb.GCSLogsClient(gcsClient),
		Client:             gcbClient,
		PrivatePool:        pool,
		BuilderName:        fmt.Sprintf("%s@%s", os.Getenv("K_SERVICE"), os.Getenv("K_REVISION")),
		TerminateOnTimeout: true,
	}
	if *gcbPrivatePoolName != "" {
		privatePoolConfig := &gcb.PrivatePoolConfig{
			Name:   *gcbPrivatePoolName,
			Region: *gcbPrivatePoolRegion,
		}
		executorConfig.PrivatePool = privatePoolConfig
	}
	executor, err := buildgcb.NewExecutor(executorConfig)
	if err != nil {
		return nil, errors.Wrap(err, "creating GCB executor")
	}
	d.GCBExecutor = executor
	d.BuildProject = *project
	d.BuildServiceAccount = *buildRemoteIdentity
	d.MetadataBucket = *metadataBucket
	if *prebuildVersion != "" {
		prebuildRepo, err := serviceid.ParseLocation(BuildRepo, *prebuildVersion)
		if err != nil {
			return nil, errors.Wrap(err, "parsing prebuild location")
		}
		// NOTE: The subdir will match the version identifier used for the service version.
		d.PrebuildConfig.Dir = prebuildRepo.Ref
	}
	d.PrebuildConfig.Bucket = *prebuildBucket
	d.PrebuildConfig.Auth = *prebuildAuth

	return &d, nil
}

func AgentCompleteInit(ctx context.Context) (*agentapiservice.AgentCompleteDeps, error) {
	var d agentapiservice.AgentCompleteDeps
	var err error
	d.FirestoreClient, err = firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &d, nil
}

func ScratchCreateInit(ctx context.Context) (*agentapiservice.ScratchCreateDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	gce, err := agentapiservice.NewComputeGCE(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "compute client")
	}
	return &agentapiservice.ScratchCreateDeps{
		Scratches: db.NewFirestoreScratch(fs),
		GCE:       gce,
		Standard: agentapiservice.ClassConfig{
			InstanceTemplate: *scratchStandardTmpl,
		},
		Jumbo: func() *agentapiservice.ClassConfig {
			if *scratchJumboTmpl == "" {
				return nil
			}
			return &agentapiservice.ClassConfig{
				InstanceTemplate: *scratchJumboTmpl,
			}
		}(),
		Zone: *scratchZone,
	}, nil
}

func ScratchGetInit(ctx context.Context) (*agentapiservice.ScratchGetDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	return &agentapiservice.ScratchGetDeps{Scratches: db.NewFirestoreScratch(fs)}, nil
}

func ScratchDeleteInit(ctx context.Context) (*agentapiservice.ScratchDeleteDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	gce, err := agentapiservice.NewComputeGCE(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "compute client")
	}
	return &agentapiservice.ScratchDeleteDeps{Scratches: db.NewFirestoreScratch(fs), GCE: gce}, nil
}

func main() {
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/session/iteration", api.Handler(AgentCreateIterationInit, agentapiservice.AgentCreateIteration))
	mux.HandleFunc("/agent/session/complete", api.Handler(AgentCompleteInit, agentapiservice.AgentComplete))

	if *scratchEnabled {
		mux.HandleFunc("/scratch/create", api.Handler(ScratchCreateInit, agentapiservice.ScratchCreate))
		mux.HandleFunc("/scratch/get", api.Handler(ScratchGetInit, agentapiservice.ScratchGet))
		mux.HandleFunc("/scratch/delete", api.Handler(ScratchDeleteInit, agentapiservice.ScratchDelete))
	}

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		log.Fatalln(err)
	}
}
