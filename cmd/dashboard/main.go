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
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api/dashboard"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
)

var (
	project        = flag.String("project", "", "GCP Project ID for storage and build resources")
	bench          = flag.String("bench", "", "Path to a benchmark file")
	port           = flag.Int("port", 8080, "port on which to serve")
	successRegex   = flag.String("success-regex", "", "Regex to determine if a rebuild is successful based on its message")
	logsBucket     = flag.String("logs-bucket", "", "GCS bucket containing build logs")
	rundexURI      = flag.String("rundex", "", "Snapshot database URI to serve rundex reads from (supported schemes: gs, file). If empty, reads Firestore directly")
	sessionsBucket = flag.String("sessions-bucket", "", "GCS bucket containing agent session transcripts")
)

var egressCfg httpegress.Config

var (
	successPat *regexp.Regexp
	benchPkgs  rundex.PackageIndex
	benchName  string
	registry   rebuild.RegistryMux   // built once at startup
	snapReader *rundex.SQLite        // process-level snapshot reader when -rundex is set
	snapCache  dashboard.Analytics   // the snapshot cache, backing the trend views
	snapReady  = make(chan struct{}) // closed once snapReader and snapCache are set

	fsReader  *rundex.FirestoreClient // rundex reads when -rundex is empty
	gcsClient *storage.Client         // set when a bucket flag is given
	execs     db.ScratchExecs         // set when -project is given
)

func DashboardInit(ctx context.Context) (*dashboard.Deps, error) {
	deps := &dashboard.Deps{
		LogsBucket:     *logsBucket,
		SessionsBucket: *sessionsBucket,
		Benchmark:      benchPkgs,
		BenchmarkName:  benchName,
		SuccessRegex:   successPat,
		Registry:       registry,
		GCSClient:      gcsClient,
		Execs:          execs,
	}
	if *rundexURI != "" {
		select {
		case <-snapReady:
		case <-ctx.Done():
			return nil, errors.Wrap(ctx.Err(), "awaiting snapshot hydration")
		}
		deps.Rundex = snapReader
		deps.Sessions = snapReader
		deps.Analytics = snapCache
	} else {
		deps.Rundex = fsReader
		deps.Sessions = fsReader
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
		// Hydration downloads the full snapshot base, so it runs behind the
		// listener: Cloud Run's startup probe needs the port bound promptly.
		// Requests gate on snapReady until the cache is served.
		go func() {
			ctx := context.Background()
			dest, err := billyx.NewResolver().DirFS(ctx, *rundexURI)
			if err != nil {
				log.Fatalf("Failed to resolve snapshot %s: %v", *rundexURI, err)
			}
			cache, err := snapshot.OpenCache(ctx, dest, 30*time.Second)
			if err != nil {
				log.Fatalf("Failed to open snapshot %s: %v", *rundexURI, err)
			}
			snapCache = cache
			snapReader = rundex.NewSQLite(cache)
			close(snapReady)
			log.Printf("Serving rundex reads from %s (fresh through %s)", *rundexURI, cache.Freshness().Format(time.RFC3339))
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
			<-stop
			cache.Close() // removes the local snapshot copy
			os.Exit(0)
		}()
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
	if *rundexURI == "" {
		fsReader, err = rundex.NewFirestore(context.Background(), *project)
		if err != nil {
			log.Fatalf("Failed to create rundex client: %v", err)
		}
	}
	if *logsBucket != "" || *sessionsBucket != "" {
		gcsClient, err = storage.NewClient(context.Background())
		if err != nil {
			log.Fatalf("Failed to create storage client: %v", err)
		}
	}
	if *project != "" {
		fs, err := firestore.NewClient(context.Background(), *project)
		if err != nil {
			log.Fatalf("Failed to create firestore client: %v", err)
		}
		execs = db.NewFirestoreScratchExecs(fs)
	}
	// Without a benchmark the index omits its status grid and recent-rebuild
	// list, which suits a deployment that serves the whole snapshot.
	if *bench != "" {
		set, err := benchmark.ReadBenchmark(*bench)
		if err != nil {
			log.Fatalf("Failed to read benchmark file: %v", err)
		}
		benchPkgs = make(rundex.PackageIndex)
		for _, p := range set.Packages {
			eco := rebuild.Ecosystem(p.Ecosystem)
			if _, ok := benchPkgs[eco]; !ok {
				benchPkgs[eco] = make(map[string]bool)
			}
			benchPkgs[eco][p.Name] = true
		}
		benchName = filepath.Base(*bench)
	}
	encoding := rebuild.FilesystemTargetEncoding

	dashboard.RegisterAssets(http.DefaultServeMux)
	// The top-level pages read only the snapshot, so their data is computed
	// once per snapshot version.
	pages := &dashboard.SnapshotPages{}
	http.HandleFunc("/favicon.ico", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/{$}", api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.PerSnapshot(pages, dashboard.Index, nil))), dashboard.IndexTmpl))
	http.HandleFunc("/resources", api.Translate(func(r *http.Request) (dashboard.ResourcesRequest, error) {
		return dashboard.ResourcesRequest{Eco: r.URL.Query().Get("eco")}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.PerSnapshot(pages, dashboard.ResourcesPage, (*dashboard.ResourcesData).Tabs))), dashboard.ResourcesTmpl)))
	http.HandleFunc("/coverage", api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.CoverageView(pages))), dashboard.CoverageTmpl))
	http.HandleFunc("/package/{ecosystem}/{package}", api.Translate(func(r *http.Request) (dashboard.PackageRequest, error) {
		t := encoding.New(rebuild.Ecosystem(r.PathValue("ecosystem")), r.PathValue("package"), "", "").Decode()
		return dashboard.PackageRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.Package)), dashboard.PackageTmpl)))
	http.HandleFunc("/package/{ecosystem}/{package}/version/{version}", api.Translate(func(r *http.Request) (dashboard.VersionRequest, error) {
		t := encoding.New(rebuild.Ecosystem(r.PathValue("ecosystem")), r.PathValue("package"), r.PathValue("version"), "").Decode()
		return dashboard.VersionRequest{
			Ecosystem: string(t.Ecosystem),
			Package:   t.Package,
			Version:   t.Version,
		}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.Version)), dashboard.VersionTmpl)))
	http.HandleFunc("/packages", api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.PerSnapshot(pages, dashboard.PackagesPage, (*dashboard.PackagesData).Tabs))), dashboard.PackagesTmpl))
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
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.Attempt)), dashboard.AttemptTmpl)))
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
	http.HandleFunc("/session/{id}", api.Translate(func(r *http.Request) (dashboard.SessionRequest, error) {
		return dashboard.SessionRequest{ID: r.PathValue("id")}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.Session)), dashboard.SessionTmpl)))
	http.HandleFunc("/session/{id}/transcript", api.Translate(func(r *http.Request) (dashboard.SessionRequest, error) {
		return dashboard.SessionRequest{ID: r.PathValue("id")}, nil
	}, api.HTMLHandler(DashboardInit, api.WithTimeout(30*time.Second, dashboard.Framed(dashboard.Session)), dashboard.TranscriptTmpl)))
	http.HandleFunc("/session/{id}/exec/{execid}/out", func(w http.ResponseWriter, r *http.Request) {
		deps, err := DashboardInit(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dashboard.HandleRawExecOutput(w, r, r.PathValue("id"), r.PathValue("execid"), deps)
	})
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
	if *rundexURI != "" {
		go pages.Warm(context.Background(), DashboardInit)
	}
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting dashboard on %s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
