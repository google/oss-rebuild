// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/google/oss-rebuild/analyzer/network/analyzerservice"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/analyzer"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var (
	analyzerURL    = flag.String("analyzer-url", "", "the Cloud Tasks queue resource path to use")
	taskQueuePath  = flag.String("task-queue", "", "the Cloud Tasks queue resource path to use")
	taskQueueEmail = flag.String("task-queue-email", "", "the service account email used as the identity for Cloud Tasks-initiated calls")
	port           = flag.Int("port", 8080, "port on which to serve")
)

func EnqueueInit(ctx context.Context) (*analyzerservice.EnqueueDeps, error) {
	queue, err := taskqueue.NewQueue(ctx, *taskQueuePath, *taskQueueEmail)
	if err != nil {
		return nil, errors.Wrap(err, "creating task queue")
	}
	return &analyzerservice.EnqueueDeps{
		Tracker:  feed.TrackerFromFunc(func(schema.TargetEvent) (bool, error) { return true, nil }),
		Analyzer: urlx.MustParse(*analyzerURL),
		Queue:    queue,
	}, nil
}

func main() {
	flag.Parse()
	http.HandleFunc("/enqueue", api.Translate(analyzer.GCSEventBodyToTargetEvent, api.Handler(EnqueueInit, analyzerservice.Enqueue)))
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalln(err)
	}
}
