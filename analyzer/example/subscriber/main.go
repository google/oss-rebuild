// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/google/oss-rebuild/analyzer/example/analyzerservice"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var (
	analyzerURL    = flag.String("analyzer-url", "", "the Cloud Tasks queue resource path to use")
	taskQueuePath  = flag.String("task-queue", "", "the Cloud Tasks queue resource path to use")
	taskQueueEmail = flag.String("task-queue-email", "", "the service account email used as the identity for Cloud Tasks-initiated calls")
)

func EnqueueInit(ctx context.Context) (*analyzerservice.EnqueueDeps, error) {
	queue, err := taskqueue.NewQueue(ctx, *taskQueuePath, *taskQueueEmail)
	if err != nil {
		return nil, errors.Wrap(err, "creating task queue")
	}
	return &analyzerservice.EnqueueDeps{
		Tracker:  feed.TrackerFromFunc(func(schema.ReleaseEvent) (bool, error) { return true, nil }),
		Analyzer: urlx.MustParse(*analyzerURL),
		Queue:    queue,
	}, nil
}

func main() {
	flag.Parse()
	http.HandleFunc("/enqueue", api.Translate(analyzerservice.RebuildMessageToReleaseEvent, api.Handler(EnqueueInit, analyzerservice.Enqueue)))
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
