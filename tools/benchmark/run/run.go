// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
)

type packageWorker interface {
	Setup(ctx context.Context)
	ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict)
}

type executor struct {
	Concurrency int
	Worker      packageWorker
}

func (ex *executor) Process(ctx context.Context, out chan schema.Verdict, packages []benchmark.Package) {
	ex.Worker.Setup(ctx)
	jobs := make(chan benchmark.Package)
	go func() {
		for _, p := range packages {
			jobs <- p
		}
		close(jobs)
	}()
	var wg sync.WaitGroup
	for range ex.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				ex.Worker.ProcessOne(ctx, p, out)
			}
		}()
	}
	wg.Wait()
	close(out)
}

type workerConfig struct {
	client   *http.Client
	url      *url.URL
	limiters map[string]<-chan time.Time
	run      string
}

type attestWorker struct {
	workerConfig
	useSyscallMonitor bool
	useNetworkProxy   bool
}

var _ packageWorker = &attestWorker{}

func (w *attestWorker) Setup(ctx context.Context) {}

func (w *attestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	if len(p.Artifacts) > 0 && len(p.Artifacts) != len(p.Versions) {
		log.Fatalf("Provided artifact slice does not match versions: %s", p.Name)
	}
	stub := api.Stub[schema.RebuildPackageRequest, schema.Verdict](w.client, w.url.JoinPath("rebuild"))
	for i, v := range p.Versions {
		<-w.limiters[p.Ecosystem]
		var artifact string
		if len(p.Artifacts) > 0 {
			artifact = p.Artifacts[i]
		}
		req := schema.RebuildPackageRequest{
			Ecosystem:         rebuild.Ecosystem(p.Ecosystem),
			Package:           p.Name,
			Version:           v,
			Artifact:          artifact,
			ID:                w.run,
			UseSyscallMonitor: w.useSyscallMonitor,
			UseNetworkProxy:   w.useNetworkProxy,
		}
		verdict, err := stub(ctx, req)
		if err != nil {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: err.Error(),
			}
		} else {
			out <- *verdict
		}
	}
}

type smoketestWorker struct {
	workerConfig
	warmup bool
}

func (w *smoketestWorker) Setup(ctx context.Context) {
	if w.warmup {
		// First, warm up the instances to ensure it can handle actual load.
		// Warm up requires the service fulfill sequentially successful version
		// requests (which hit both the API and the builder jobs).
		stub := api.Stub[schema.VersionRequest, schema.VersionResponse](w.client, w.url.JoinPath("version"))
		req := schema.VersionRequest{Service: "build-local"}
		for i := 0; i < 5; {
			if _, err := stub(ctx, req); err != nil {
				i = 0
			} else {
				i++
			}
		}
	}
}

func (w *smoketestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	<-w.limiters[p.Ecosystem]
	stub := api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](w.client, w.url.JoinPath("smoketest"))
	req := schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(p.Ecosystem),
		Package:   p.Name,
		Versions:  p.Versions,
		ID:        w.run,
	}
	resp, err := stub(ctx, req)
	if err != nil {
		errMsg := err.Error()
		for _, v := range p.Versions {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
				},
				Message: errMsg,
			}
		}
		return
	}
	for _, v := range resp.Verdicts {
		out <- v
	}
}

func defaultLimiters() map[string]<-chan time.Time {
	return map[string]<-chan time.Time{
		"debian": time.Tick(time.Second),
		"pypi":   time.Tick(time.Second),
		"npm":    time.Tick(2 * time.Second),
		"maven":  time.Tick(2 * time.Second),
		// NOTE: cratesio needs to be especially slow given our registry API
		// constraint of 1QPS. At minimum, we expect to make 4 calls per test.
		"cratesio": time.Tick(8 * time.Second),
	}
}

func isCloudRun(u *url.URL) bool {
	return u != nil && strings.HasSuffix(u.Host, ".run.app")
}

type RunBenchOpts struct {
	Mode              schema.ExecutionMode
	RunID             string
	MaxConcurrency    int
	UseSyscallMonitor bool
	UseNetworkProxy   bool
}

func RunBench(ctx context.Context, client *http.Client, apiURL *url.URL, set benchmark.PackageSet, opts RunBenchOpts) (<-chan schema.Verdict, error) {
	if opts.RunID == "" {
		return nil, errors.New("opts.RunID must be set")
	}
	if (opts.UseNetworkProxy || opts.UseSyscallMonitor) && opts.Mode != schema.AttestMode {
		return nil, errors.New("cannot enable network proxy or syscall monitor for non-attest mode")
	}
	conf := workerConfig{
		client:   client,
		url:      apiURL,
		limiters: defaultLimiters(),
		run:      opts.RunID,
	}
	ex := executor{Concurrency: opts.MaxConcurrency}
	switch opts.Mode {
	case schema.SmoketestMode:
		ex.Worker = &smoketestWorker{
			workerConfig: conf,
			warmup:       isCloudRun(apiURL),
		}
	case schema.AttestMode:
		ex.Worker = &attestWorker{
			workerConfig:      conf,
			useSyscallMonitor: opts.UseSyscallMonitor,
			useNetworkProxy:   opts.UseNetworkProxy,
		}
	default:
		return nil, fmt.Errorf("invalid mode: %s", string(opts.Mode))
	}
	verdictChan := make(chan schema.Verdict)
	go ex.Process(ctx, verdictChan, set.Packages)
	return verdictChan, nil
}

func RunBenchAsync(ctx context.Context, set benchmark.PackageSet, mode schema.ExecutionMode, apiURL *url.URL, runID string, queue taskqueue.Queue) error {
	for _, p := range set.Packages {
		if mode == schema.AttestMode {
			for i, v := range p.Versions {
				req := schema.RebuildPackageRequest{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
					ID:        runID,
				}
				if len(p.Artifacts) > 0 {
					req.Artifact = p.Artifacts[i]
				}
				if _, err := queue.Add(ctx, apiURL.JoinPath("rebuild").String(), req); err != nil {
					return errors.Wrap(err, "queing rebuild task")
				}
			}
		} else if mode == schema.SmoketestMode {
			req := schema.SmoketestRequest{
				Ecosystem: rebuild.Ecosystem(p.Ecosystem),
				Package:   p.Name,
				Versions:  p.Versions,
				ID:        runID,
			}
			if _, err := queue.Add(ctx, apiURL.JoinPath("smoketest").String(), req); err != nil {
				return errors.Wrap(err, "queing smoketest task")
			}
		}
	}
	return nil
}
