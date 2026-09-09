// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// snapshot-rollup rebuilds the analytics snapshot from Firestore and exits.
// Its scan writes an attempt-history-sized SQLite file to the in-memory
// filesystem, so it runs as a Cloud Run job with its own memory and timeout.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/buildinfo"
	"github.com/google/oss-rebuild/internal/snapshot"
)

var (
	project      = flag.String("project", "", "GCP project holding the Firestore records to scan")
	analyticsURI = flag.String("analytics-uri", "", "URI the snapshot is published under (supported schemes: gs, file)")
	signalsDBURI = flag.String("signals-db-uri", "", "URI the signal database publishes under, ingested at rollup (supported schemes: gs, file). Empty skips signal ingest")
)

func main() {
	flag.Parse()
	if *project == "" || *analyticsURI == "" {
		log.Fatal("project and analytics-uri are required")
	}
	ctx := context.Background()
	src, err := snapshot.NewFirestoreSource(ctx, *project)
	if err != nil {
		log.Fatalf("creating snapshot source: %v", err)
	}
	if *signalsDBURI != "" {
		if src.SignalsDB, err = billyx.NewResolver().DirFS(ctx, *signalsDBURI); err != nil {
			log.Fatalf("opening signals database: %v", err)
		}
	}
	dest, err := billyx.NewResolver().DirFS(ctx, *analyticsURI)
	if err != nil {
		log.Fatalf("opening analytics destination: %v", err)
	}
	res, err := snapshot.Rollup(ctx, src, dest, snapshot.Options{Project: *project, ToolVersion: buildinfo.Version})
	if err != nil {
		log.Fatalf("running rollup: %v", err)
	}
	log.Printf("published schema v%d built at %s, watermark %s, rows %v", snapshot.SchemaVersion, res.Meta.BuiltAt.Format(time.RFC3339), res.Meta.Watermark.Format(time.RFC3339), res.RowCounts)
}
