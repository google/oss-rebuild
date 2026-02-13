// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/google/oss-rebuild/analyzer/example/analyzerservice"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/analyzer"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var (
	findingsBucket = flag.String("findings-bucket", "", "the GCS bucket to write out findings")
	taskQueuePath  = flag.String("task-queue", "", "the Cloud Tasks queue resource path to use")
	taskQueueEmail = flag.String("task-queue-email", "", "the service account email used as the identity for Cloud Tasks-initiated calls")
	port           = flag.Int("port", 8080, "port on which to serve")
)

// Link-time configured service identity
var (
	// Repo from which the service was built
	BuildRepo string
	// Golang version identifier of the service container builds
	BuildVersion string
)

func EnqueueInit(ctx context.Context) (*analyzerservice.EnqueueDeps, error) {
	queue, err := taskqueue.NewQueue(ctx, *taskQueuePath, *taskQueueEmail)
	if err != nil {
		return nil, errors.Wrap(err, "creating task queue")
	}
	return &analyzerservice.EnqueueDeps{
		Tracker:  feed.TrackerFromFunc(func(schema.TargetEvent) (bool, error) { return true, nil }),
		Analyzer: urlx.MustParse("/analyze"),
		Queue:    queue,
	}, nil
}

func AnalyzerInit(ctx context.Context) (*analyzerservice.AnalyzerDeps, error) {
	ctx = context.WithValue(ctx, rebuild.RunID, "")
	findings, err := rebuild.NewGCSStore(ctx, "gcs://"+*findingsBucket)
	if err != nil {
		return nil, errors.Wrap(err, "creating findings asset store")
	}
	return &analyzerservice.AnalyzerDeps{
		BuildRepo:    urlx.MustParse(BuildRepo),
		BuildVersion: BuildVersion,
		Findings:     findings,
	}, nil
}

func main() {
	flag.Parse()
	http.HandleFunc("/enqueue", api.Translate(analyzer.GCSEventBodyToTargetEvent, api.Handler(EnqueueInit, analyzerservice.Enqueue)))
	http.HandleFunc("/analyze", api.Handler(AnalyzerInit, analyzerservice.Analyze))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalln(err)
	}
}
