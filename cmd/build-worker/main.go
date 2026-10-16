// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// build-worker runs the extended rebuilds the API queues on a Pub/Sub topic.
// Multitenant as most time is RPC waits on inference and build.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/api/apiservice"
	"github.com/google/oss-rebuild/internal/buildinfo"
	"github.com/google/oss-rebuild/internal/cache"
	"github.com/google/oss-rebuild/internal/httpegress"
	"github.com/google/oss-rebuild/internal/serviceid"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var (
	project         = flag.String("project", "", "GCP project of the subscription")
	subscription    = flag.String("subscription", "", "Pub/Sub subscription to pull build tasks from")
	buildsPerWorker = flag.Int("builds-per-worker", 0, "Rebuilds to run at once. A waiting rebuild holds tens of MiB and a few percent of a vCPU.")
)

// maxBuild bounds a task's lease, like the rebuild job's day-long timeout.
const maxBuild = 24 * time.Hour

func main() {
	flag.Parse()
	if *buildsPerWorker <= 0 {
		log.Fatal("--builds-per-worker must be positive")
	}
	// Attestations name this binary as the service source, so this can't come
	// from the task's config with the rest of the deps.
	serviceRepo, err := serviceid.ParseLocation(buildinfo.Repo, buildinfo.Version)
	if err != nil {
		log.Fatalf("parsing service location: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	httpegress.AwaitReady(ctx, http.DefaultClient)
	client, err := pubsub.NewClient(ctx, *project)
	if err != nil {
		log.Fatalf("creating pubsub client: %v", err)
	}
	defer client.Close()
	sub := client.Subscriber(*subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = *buildsPerWorker
	sub.ReceiveSettings.MaxExtension = maxBuild
	// Tasks with the same deps config share one set of clients.
	var clients cache.CoalescingMemoryCache[schema.RebuildDepsConfig, *apiservice.RebuildPackageDeps]
	depsFor := func(cfg schema.RebuildDepsConfig) (*apiservice.RebuildPackageDeps, error) {
		return clients.GetOrSet(cfg, func() (*apiservice.RebuildPackageDeps, error) {
			d, err := apiservice.MakeRebuildPackageDeps(context.Background(), &cfg)
			if err != nil {
				return nil, errors.Wrap(err, "making rebuild deps")
			}
			d.ServiceRepo = serviceRepo
			return d, nil
		})
	}
	// Rebuilds run detached from ctx: one interrupted by shutdown is not acked,
	// so Pub/Sub redelivers it to another worker.
	err = sub.Receive(ctx, func(_ context.Context, m *pubsub.Message) {
		var task apiservice.BuildTask
		if err := json.Unmarshal(m.Data, &task); err != nil {
			log.Printf("dropping malformed build task: %v", err)
			m.Ack()
			return
		}
		if err := run(context.Background(), task, depsFor); err != nil {
			log.Printf("retrying build task: %v", err)
			m.Nack()
			return
		}
		m.Ack()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("receiving build tasks: %v", err)
	}
}

// depsFunc makes the deps for a deps config.
type depsFunc func(schema.RebuildDepsConfig) (*apiservice.RebuildPackageDeps, error)

// run runs one build task. RebuildPackage records failures on the attempt, so
// the caller acks every finished task. An error means no rebuild ran, so
// nothing has recorded the failure and the task should be retried.
func run(ctx context.Context, task apiservice.BuildTask, depsFor depsFunc) error {
	shared, err := depsFor(task.Deps)
	if err != nil {
		return errors.Wrapf(err, "getting deps for %s", task.OpID)
	}
	// Each rebuild gets its own in-memory asset store so assets do not pile up.
	deps := *shared
	deps.LocalMetadataStore = rebuild.NewFilesystemAssetStore(memfs.New())
	if task.Request.BuildTimeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, task.Request.BuildTimeout)
		defer cancel()
	}
	if _, err := apiservice.RebuildPackage(ctx, task.Request, &deps); err != nil {
		return errors.Wrapf(err, "starting rebuild %s", task.OpID)
	}
	return nil
}
