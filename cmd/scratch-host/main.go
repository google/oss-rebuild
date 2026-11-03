// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command scratch-host is the scratch host broker. So far it is the
// listener, an open health check and an ID-token check on every other route.
//
// TODO: Boot the guest and tear it down.
// TODO: Reach into the guest over ssh on the host-only bridge.
// TODO: Serve the exec API.
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
	// Only the health check is open.
	mw := idauth.Middleware(idauth.NewGoogleValidator(*callerSA, *audience))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	mux.Handle("/", mw(http.NotFoundHandler()))
	srv := &http.Server{Addr: *listen, Handler: mux}
	drained := make(chan struct{}) // ListenAndServe returns as Shutdown starts, not when it ends
	go func() {
		defer close(drained)
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
	<-drained
}
