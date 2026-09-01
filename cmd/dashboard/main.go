// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api/dashboard"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
)

var (
	project         = flag.String("project", "", "GCP Project ID for storage and build resources")
	bench           = flag.String("bench", "", "Path to a benchmark file")
	trackedPackages = flag.String("tracked-packages", "", "JSON string of TrackedPackageSet")
	port            = flag.Int("port", 8080, "port on which to serve")
	successRegex    = flag.String("success-regex", "", "Regex to determine if a rebuild is successful based on its message")
	logsBucket      = flag.String("logs-bucket", "", "GCS bucket containing build logs")
	rundexURI       = flag.String("rundex", "", "Snapshot database URI to serve rundex reads from (supported schemes: gs, file). If empty, reads Firestore directly")
)

var egressCfg httpegress.Config

var (
	successPat *regexp.Regexp
	tracked    feed.TrackedPackageIndex
	benchName  string
	registry   rebuild.RegistryMux // built once at startup
	snapReader *rundex.SQLite      // process-level snapshot reader when -rundex is set
)

func DashboardInit(ctx context.Context) (*dashboard.Deps, error) {
	deps := &dashboard.Deps{
		LogsBucket:    *logsBucket,
		Tracked:       tracked,
		BenchmarkName: benchName,
		SuccessRegex:  successPat,
		Registry:      registry,
	}
	if snapReader != nil {
		deps.Rundex = snapReader
		deps.Sessions = snapReader
	} else {
		rundexClient, err := rundex.NewFirestore(ctx, *project)
		if err != nil {
			return nil, err
		}
		deps.Rundex = rundexClient
		deps.Sessions = rundexClient
	}
	if *logsBucket != "" {
		storageClient, err := storage.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		deps.GCSClient = storageClient
	}
	return deps, nil
}

func main() {
	egressCfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if *project == "" && *rundexURI == "" {
		log.Fatal("Must provide -project (or -rundex to serve from a snapshot)")
	}
	if *rundexURI != "" {
		ctx := context.Background()
		dest, err := billyx.NewResolver().DirFS(ctx, *rundexURI)
		if err != nil {
			log.Fatalf("Failed to resolve snapshot %s: %v", *rundexURI, err)
		}
		cache, err := snapshot.OpenCache(ctx, dest, 30*time.Second)
		if err != nil {
			log.Fatalf("Failed to open snapshot %s: %v", *rundexURI, err)
		}
		defer cache.Close()
		snapReader = rundex.NewSQLite(cache)
		log.Printf("Serving rundex reads from %s (fresh through %s)", *rundexURI, cache.Freshness().Format(time.RFC3339))
	}
	if *logsBucket == "" {
		log.Printf("Warning: -logs-bucket not provided, log viewing will be unavailable")
	}
	if *successRegex != "" {
		var err error
		successPat, err = regexp.Compile(*successRegex)
		if err != nil {
			log.Fatalf("Failed to compile success regex: %v", err)
		}
	}
	egressClient, err := httpegress.MakeClient(context.Background(), egressCfg)
	if err != nil {
		log.Fatalf("Failed to create egress client: %v", err)
	}
	registry = meta.NewRegistryMux(egressClient)
	if (*bench != "") == (*trackedPackages != "") {
		log.Fatalf("Must provide exactly one of -bench or -tracked-packages")
	}
	if *bench != "" {
		set, err := benchmark.ReadBenchmark(*bench)
		if err != nil {
			log.Fatalf("Failed to read benchmark file: %v", err)
		}
		tracked = make(feed.TrackedPackageIndex)
		for _, p := range set.Packages {
			eco := rebuild.Ecosystem(p.Ecosystem)
			if _, ok := tracked[eco]; !ok {
				tracked[eco] = make(map[string]bool)
			}
			tracked[eco][p.Name] = true
		}
		benchName = filepath.Base(*bench)
	} else if *trackedPackages != "" {
		var tps feed.TrackedPackageSet
		if err := json.Unmarshal([]byte(*trackedPackages), &tps); err != nil {
			log.Fatalf("Failed to unmarshal tracked-packages: %v", err)
		}
		tracked = tps.Index()
		benchName = "Tracked Packages"
	}
	encoding := rebuild.FilesystemTargetEncoding

	dashboard.RegisterAssets(http.DefaultServeMux)
	http.HandleFunc("/", api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Index), dashboard.IndexTmpl))
	http.HandleFunc("/package/{ecosystem}/{package}", api.Translate(func(r *http.Request) (dashboard.PackageRequest, error) {
		t := encoding.New(rebuild.Ecosystem(r.PathValue("ecosystem")), r.PathValue("package"), "", "").Decode()
		// TODO: Make this param and field name more precise.
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		return dashboard.PackageRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Offset:    offset,
			Expanded:  r.URL.Query().Get("expanded") != "",
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Package), dashboard.PackageTmpl)))
	http.HandleFunc("/package/{ecosystem}/{package}/version/{version}", api.Translate(func(r *http.Request) (dashboard.VersionRequest, error) {
		t := encoding.New(rebuild.Ecosystem(r.PathValue("ecosystem")), r.PathValue("package"), r.PathValue("version"), "").Decode()
		return dashboard.VersionRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Version:   t.Version,
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Version), dashboard.VersionTmpl)))
	http.HandleFunc("/attempt/{ecosystem}/{package}/{version}/{artifact}/{runid}", api.Translate(func(r *http.Request) (dashboard.AttemptRequest, error) {
		t := encoding.New(
			rebuild.Ecosystem(r.PathValue("ecosystem")),
			r.PathValue("package"),
			r.PathValue("version"),
			r.PathValue("artifact"),
		).Decode()
		return dashboard.AttemptRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
			RunID:     r.PathValue("runid"),
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Attempt), dashboard.AttemptTmpl)))
	http.HandleFunc("/attempt/{ecosystem}/{package}/{version}/{artifact}/{runid}/build-logs/", api.Translate(func(r *http.Request) (dashboard.LogsRequest, error) {
		t := encoding.New(
			rebuild.Ecosystem(r.PathValue("ecosystem")),
			r.PathValue("package"),
			r.PathValue("version"),
			r.PathValue("artifact"),
		).Decode()
		return dashboard.LogsRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
			RunID:     r.PathValue("runid"),
		}, nil
	}, api.HTMLHandler(DashboardInit, dashboard.Logs, dashboard.LogsTmpl)))
	http.HandleFunc("/attempt/{ecosystem}/{package}/{version}/{artifact}/{runid}/build-logs/raw/", func(w http.ResponseWriter, r *http.Request) {
		deps, err := DashboardInit(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		t := encoding.New(
			rebuild.Ecosystem(r.PathValue("ecosystem")),
			r.PathValue("package"),
			r.PathValue("version"),
			r.PathValue("artifact"),
		).Decode()
		req := dashboard.LogsRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
			RunID:     r.PathValue("runid"),
		}
		dashboard.HandleRawLogs(w, r, req, deps)
	})
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting dashboard on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
