// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// PyPI RSS Subscriber for OSS Rebuild
// This tool is a long-running service that fetches updates from PyPI's RSS feed,
// and adds rebuild attempts into a task queue for any release of a package that's considered "tracked".
// See https://docs.pypi.org/api/feeds/ for more details about the particular feed.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/pypi_rss/listener"
	"github.com/pkg/errors"
)

const (
	pypiUpdatesURL = "https://pypi.org/rss/updates.xml"
)

var (
	apiURI         = flag.String("api", "", "OSS Rebuild API endpoint URI")
	executionMode  = flag.String("execution-mode", "attest", "[attest|smoketest] the mode in which to execute rebuilds for incoming python updates")
	taskQueuePath  = flag.String("task-queue", "", "the Cloud Tasks queue resource path to use")
	taskQueueEmail = flag.String("task-queue-email", "", "the service account email used as the identity for Cloud Tasks-initiated calls")
	refBench       = flag.String("benchmark", "", "a benchmark containing the tracked packages")
	sleepTime      = flag.Duration("sleep-time", 5*time.Minute, "how long to sleep between polling the feed")
)

func main() {
	flag.Parse()
	ctx := context.Background()
	queue, err := taskqueue.NewQueue(ctx, *taskQueuePath, *taskQueueEmail)
	if err != nil {
		log.Fatal(errors.Wrap(err, "creating task queue"))
	}
	var tracker feed.Tracker
	{
		tracked := make(map[rebuild.Ecosystem]map[string]bool)
		tracked[rebuild.PyPI] = make(map[string]bool)
		if ps, err := benchmark.ReadBenchmark(*refBench); err != nil {
			log.Fatal(errors.Wrapf(err, "reading benchmark file %s", *refBench))
		} else {
			for _, p := range ps.Packages {
				tracked[rebuild.PyPI][p.Name] = true
			}
		}
		tracker = feed.TrackerFromFunc(func(e schema.TargetEvent) (bool, error) {
			if _, ok := tracked[e.Ecosystem]; !ok {
				return false, nil
			}
			tracked, ok := tracked[e.Ecosystem][e.Package]
			return ok && tracked, nil
		})
	}
	mode := schema.ExecutionMode(*executionMode)
	if mode != schema.AttestMode && mode != schema.SmoketestMode {
		log.Fatalf("--execution-mode must be '%s' or '%s' but got '%s'", schema.SmoketestMode, schema.AttestMode, *executionMode)
	}
	l := listener.NewListener(
		http.DefaultClient,
		pypiUpdatesURL,
		tracker,
		urlx.MustParse(*apiURI),
		mode,
		queue,
	)
	for {
		if err := l.Poll(ctx); err != nil {
			log.Printf("Failed to check latest feed: %v", err)
			return
		}
		time.Sleep(*sleepTime)
	}
}
