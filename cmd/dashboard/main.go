// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/dashboard"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
)

var (
	project      = flag.String("project", "", "GCP Project ID for storage and build resources")
	bench        = flag.String("bench", "", "Path to a benchmark file")
	port         = flag.Int("port", 8080, "port on which to serve")
	successRegex = flag.String("success-regex", "", "Regex to determine if a rebuild is successful based on its message")
	logsBucket   = flag.String("logs-bucket", "", "GCS bucket containing build logs")
)

var (
	benchSet   *benchmark.PackageSet
	benchName  string
	successPat *regexp.Regexp
)

func DashboardInit(ctx context.Context) (*dashboard.Deps, error) {
	rundexClient, err := rundex.NewFirestore(ctx, *project)
	if err != nil {
		return nil, err
	}
	var storageClient *storage.Client
	if *logsBucket != "" {
		storageClient, err = storage.NewClient(ctx)
		if err != nil {
			return nil, err
		}
	}
	return &dashboard.Deps{
		Rundex:        rundexClient,
		GCSClient:     storageClient,
		LogsBucket:    *logsBucket,
		Benchmark:     benchSet,
		BenchmarkName: benchName,
		SuccessRegex:  successPat,
	}, nil
}

func main() {
	flag.Parse()

	if *project == "" {
		log.Fatal("Must provide -project")
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

	if *bench != "" {
		set, err := benchmark.ReadBenchmark(*bench)
		if err != nil {
			log.Fatalf("Failed to read benchmark file: %v", err)
		}
		benchSet = &set
		benchName = filepath.Base(*bench)
	}

	encoding := rebuild.FilesystemTargetEncoding

	http.HandleFunc("/", api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Index), dashboard.IndexTmpl))
	http.HandleFunc("/package/{ecosystem}/{package}", api.Translate(func(r *http.Request) (dashboard.PackageRequest, error) {
		t := encoding.New(rebuild.Ecosystem(r.PathValue("ecosystem")), r.PathValue("package"), "", "").Decode()
		return dashboard.PackageRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Package), dashboard.PackageTmpl)))
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
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Logs), dashboard.LogsTmpl)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting dashboard on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
