// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command scratch-worker is the per-VM scratch worker. It serves
// /exec/start, /exec/op/status, /exec/op/output, /healthz, and /stat over
// HTTP. The worker process makes no GCP API calls of its own: on public
// deployments the VM has no attached SA, and on private deployments the VM's
// SA is scoped narrowly to GCS read for bootstrap fetching of this binary.
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
	workdir          = flag.String("workdir", "/home/builder", "default cwd for execs")
	tempDir          = flag.String("temp-dir", "", "temp directory for exec stdout/stderr capture; empty = OS default")
	dockerSocketPath = flag.String("docker-socket", "/var/run/docker.sock", "path probed by /stat for Docker daemon presence")
	diskPaths        = flag.String("disk-paths", "", "comma-separated mount paths reported by /stat")
	listen           = flag.String("listen", ":8080", "listen address")
)

// Process-wide deps, built once at startup.
var (
	execDeps   *scratchworkerservice.ExecDeps
	statusDeps *scratchworkerservice.StatusDeps
	outputDeps *scratchworkerservice.OutputDeps
	statDeps   *scratchworkerservice.StatDeps
	store      *scratchworkerservice.ExecStore
)

func execInit(_ context.Context) (*scratchworkerservice.ExecDeps, error)     { return execDeps, nil }
func statusInit(_ context.Context) (*scratchworkerservice.StatusDeps, error) { return statusDeps, nil }
func outputInit(_ context.Context) (*scratchworkerservice.OutputDeps, error) { return outputDeps, nil }
func statInit(_ context.Context) (*scratchworkerservice.StatDeps, error)     { return statDeps, nil }

func main() {
	flag.Parse()
	if *callerSA == "" {
		log.Fatalf("--caller-sa is required")
	}
	if *audience == "" {
		log.Fatalf("--audience is required")
	}

	store = scratchworkerservice.NewExecStore()
	execDeps = &scratchworkerservice.ExecDeps{
		Store:   store,
		TempDir: *tempDir,
		Workdir: *workdir,
	}
	statusDeps = &scratchworkerservice.StatusDeps{Store: store}
	outputDeps = &scratchworkerservice.OutputDeps{Store: store}
	statDeps = &scratchworkerservice.StatDeps{
		DockerSocketPath: *dockerSocketPath,
		DiskPaths:        splitCSV(*diskPaths),
	}

	mw := idauth.Middleware(idauth.NewGoogleValidator(*callerSA, *audience))

	mux := http.NewServeMux()
	mux.Handle("/exec/start", mw(api.Handler(execInit, scratchworkerservice.ExecStart)))
	mux.Handle("/exec/op/status", mw(api.Handler(statusInit, scratchworkerservice.Status)))
	mux.Handle("/exec/op/output", mw(api.StreamHandler(outputInit, scratchworkerservice.Output)))
	// TODO: Add /exec/op/kill
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
