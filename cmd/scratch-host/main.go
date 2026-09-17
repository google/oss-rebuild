// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command scratch-host is the per-host scratch broker: the one process on
// a scratch host that boots the QEMU microVM guest for the host's session
// and serves the exec API on the guest's behalf. This is the listener, its
// health check and the caller authentication every other route sits
// behind.
//
// TODO: Boot the guest and tear it down (/guest/admit, /guest/{id}, /guests).
// TODO: Reach into the guest over ssh on the host-only bridge.
// TODO: Serve the exec API per session under /guest/{id}/exec/.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/oss-rebuild/internal/api/idauth"
)

var (
	callerSA = flag.String("caller-sa", "", "caller service account email (sender of incoming requests)")
	audience = flag.String("audience", "", "expected ID-token audience (e.g. https://builder/<host-name>)")
	listen   = flag.String("listen", ":8080", "listen address")
)

func main() {
	flag.Parse()
	if *callerSA == "" {
		log.Fatalf("--caller-sa is required")
	}
	if *audience == "" {
		log.Fatalf("--audience is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// The health check stays open: the caller probes it before any caller
	// context exists. Everything else is gated on the caller's ID token.
	mw := idauth.Middleware(idauth.NewGoogleValidator(*callerSA, *audience))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	mux.Handle("/", mw(http.NotFoundHandler()))
	srv := &http.Server{Addr: *listen, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("scratch-host listening on %s", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
