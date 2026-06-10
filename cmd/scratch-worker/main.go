// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command scratch-worker is the per-VM scratch worker. It serves
// /healthz and /stat over HTTP. The worker has no GCP credentials of
// its own; the VM template should set service_account = null.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/scratchworkerservice"
	"github.com/google/oss-rebuild/internal/api/scratchworkerservice/idauth"
)

var (
	callerSA         = flag.String("caller-sa", "", "caller service account email (sender of incoming requests)")
	audience         = flag.String("audience", "", "expected ID-token audience (e.g. https://builder/<vm-name>)")
	dockerSocketPath = flag.String("docker-socket", "/var/run/docker.sock", "path probed by /stat for Docker daemon presence")
	diskPaths        = flag.String("disk-paths", "", "comma-separated mount paths reported by /stat")
	listen           = flag.String("listen", ":8080", "listen address")
)

// statDeps holds the singleton built once at startup.
var statDeps *scratchworkerservice.StatDeps

func statInit(_ context.Context) (*scratchworkerservice.StatDeps, error) { return statDeps, nil }

func main() {
	flag.Parse()
	if *callerSA == "" {
		log.Fatalf("--caller-sa is required")
	}
	if *audience == "" {
		log.Fatalf("--audience is required")
	}

	statDeps = &scratchworkerservice.StatDeps{
		DockerSocketPath: *dockerSocketPath,
		DiskPaths:        splitCSV(*diskPaths),
	}

	mw := idauth.Middleware(idauth.NewGoogleValidator(*callerSA, *audience))

	mux := http.NewServeMux()
	mux.Handle("/stat", mw(api.Handler(statInit, scratchworkerservice.Stat)))
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })

	log.Printf("scratch-worker listening on %s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
