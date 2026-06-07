// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api/agentapiservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/serviceid"
	"github.com/google/oss-rebuild/pkg/act/api"
	buildgcb "github.com/google/oss-rebuild/pkg/build/gcb"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/idtoken"
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
	scratchEnabled       = flag.Bool("scratch-enabled", false, "register the scratch routes")
	scratchZones         = flag.String("scratch-zones", "", "comma-separated, ordered list of GCE zones to try for scratch VMs (required when scratch enabled); first listed is preferred, later zones used only on stockout fallthrough")
	scratchWorkerPort    = flag.Int("scratch-worker-port", 8080, "port the worker listens on")
	scratchStandardTmpl  = flag.String("scratch-instance-standard-template", "", "GCE instance template URL for the standard machine class (required when scratch enabled)")
	scratchJumboTmpl     = flag.String("scratch-instance-jumbo-template", "", "GCE instance template URL for the jumbo machine class (optional)")
	scratchOutputBucket  = flag.String("scratch-output-bucket", "", "GCS bucket the broker writes exec output into (required when --scratch-enabled)")
	scratchIdleThreshold = flag.Duration("scratch-idle-threshold", 30*time.Minute, "scratches whose LastUsed is older than this and that have no in-deadline pending exec are reaped")
	scratchOpDeadline    = flag.Duration("scratch-op-deadline", 2*time.Hour, "default and maximum exec duration, stamped on each exec; bounds how long a pending exec exempts its scratch from idle reaping")
)

// parseScratchZones splits --scratch-zones on commas, trimming whitespace
// and dropping empty entries.
func parseScratchZones() []string {
	parts := strings.Split(*scratchZones, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Binary-wide singleton: ScratchCreateInit runs per request, so a
// Deps-owned cooldown would be discarded each call.
var scratchCooldown = agentapiservice.NewZoneCooldown(0)

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

// scratchWorkerDialer mints an ID-token-bearing HTTP client per
// scratch with audience "https://builder/<vm-name>", the audience each
// worker is configured to verify.
func scratchWorkerDialer(ctx context.Context, workerPort int) agentapiservice.WorkerDialer {
	return func(s schema.Scratch) (httpx.BasicClient, *url.URL, error) {
		audience := fmt.Sprintf("https://builder/%s", s.VMName)
		c, err := idtoken.NewClient(ctx, audience)
		if err != nil {
			return nil, nil, errors.Wrap(err, "idtoken client")
		}
		u, err := url.Parse(fmt.Sprintf("http://%s:%d", s.InternalIP, workerPort))
		if err != nil {
			return nil, nil, err
		}
		return c, u, nil
	}
}

// scratchHealthProbe pings http://<ip>:<workerPort>/healthz. Worker
// /healthz is unauthenticated by design.
func scratchHealthProbe(workerPort int) agentapiservice.HealthProbe {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(ctx context.Context, ip string) error {
		u := fmt.Sprintf("http://%s:%d/healthz", ip, workerPort)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.Errorf("healthz status %d", resp.StatusCode)
		}
		return nil
	}
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
	zones := parseScratchZones()
	if len(zones) == 0 {
		return nil, errors.New("--scratch-zones is required when --scratch-enabled")
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
		Zones:       zones,
		Cooldown:    scratchCooldown,
		HealthProbe: scratchHealthProbe(*scratchWorkerPort),
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

func ScratchExecCreateInit(ctx context.Context) (*agentapiservice.ScratchExecCreateDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	return &agentapiservice.ScratchExecCreateDeps{
		Scratches:    db.NewFirestoreScratch(fs),
		Execs:        db.NewFirestoreScratchExecs(fs),
		WorkerDialer: scratchWorkerDialer(ctx, *scratchWorkerPort),
		OutputBucket: *scratchOutputBucket,
		OpTimeout:    *scratchOpDeadline,
	}, nil
}

func ScratchExecGetInit(ctx context.Context) (*agentapiservice.ScratchExecGetDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "storage client")
	}
	execs := db.NewFirestoreScratchExecs(fs)
	return &agentapiservice.ScratchExecGetDeps{
		Scratches: db.NewFirestoreScratch(fs),
		Execs:     execs,
		Syncer:    agentapiservice.NewGCSSyncer(gcs, *scratchOutputBucket, execs, scratchWorkerDialer(ctx, *scratchWorkerPort)),
	}, nil
}

func ScratchReapInit(ctx context.Context) (*agentapiservice.ScratchReapDeps, error) {
	fs, err := firestore.NewClient(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "firestore client")
	}
	gce, err := agentapiservice.NewComputeGCE(ctx, *project)
	if err != nil {
		return nil, errors.Wrap(err, "compute client")
	}
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "storage client")
	}
	execs := db.NewFirestoreScratchExecs(fs)
	return &agentapiservice.ScratchReapDeps{
		Scratches:     db.NewFirestoreScratch(fs),
		Execs:         execs,
		GCE:           gce,
		Syncer:        agentapiservice.NewGCSSyncer(gcs, *scratchOutputBucket, execs, scratchWorkerDialer(ctx, *scratchWorkerPort)),
		IdleThreshold: *scratchIdleThreshold,
	}, nil
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
		mux.HandleFunc("/scratch/exec/op/create", api.Handler(ScratchExecCreateInit, agentapiservice.ScratchExecCreate))
		mux.HandleFunc("/scratch/exec/op/get", api.Handler(ScratchExecGetInit, agentapiservice.ScratchExecGet))
		mux.HandleFunc("/scratch/reap", api.Handler(ScratchReapInit, agentapiservice.ScratchReap))
	}

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		log.Fatalln(err)
	}
}
