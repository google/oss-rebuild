// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/taskqueue"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
)

// ExecutionService defines the contract for services that can execute rebuilds or smoketests.
type ExecutionService interface {
	RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest) (*schema.Verdict, error)
	SmoketestPackage(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error)
	// Warmup can be called to prepare the service, e.g., for remote Cloud Run instances.
	Warmup(ctx context.Context)
}

// remoteExecutionService interacts with a remote benchmark execution API.
type remoteExecutionService struct {
	rebuildStub   api.StubT[schema.RebuildPackageRequest, schema.Verdict]
	smoketestStub api.StubT[schema.SmoketestRequest, schema.SmoketestResponse]
	versionStub   api.StubT[schema.VersionRequest, schema.VersionResponse]
}

// NewRemoteExecutionService creates a new service for remote API execution.
func NewRemoteExecutionService(client *http.Client, baseURL *url.URL) ExecutionService {
	return &remoteExecutionService{
		rebuildStub:   api.Stub[schema.RebuildPackageRequest, schema.Verdict](client, baseURL.JoinPath("rebuild")),
		smoketestStub: api.Stub[schema.SmoketestRequest, schema.SmoketestResponse](client, baseURL.JoinPath("smoketest")),
		versionStub:   api.Stub[schema.VersionRequest, schema.VersionResponse](client, baseURL.JoinPath("version")),
	}
}

func (s *remoteExecutionService) RebuildPackage(ctx context.Context, req schema.RebuildPackageRequest) (*schema.Verdict, error) {
	return s.rebuildStub(ctx, req)
}

func (s *remoteExecutionService) SmoketestPackage(ctx context.Context, req schema.SmoketestRequest) (*schema.SmoketestResponse, error) {
	return s.smoketestStub(ctx, req)
}

func (s *remoteExecutionService) Warmup(ctx context.Context) {
	log.Println("Warming up remote service...")
	req := schema.VersionRequest{Service: "build-local"}
	for i := 0; i < 5; {
		if _, err := s.versionStub(ctx, req); err != nil {
			log.Printf("Warmup attempt failed: %v. Retrying...\n", err)
			i = 0
			time.Sleep(1 * time.Second)
		} else {
			i++
		}
	}
	log.Println("Warmup complete.")
}

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
	execService ExecutionService
	limiters    map[string]<-chan time.Time
	runID       string
}

type attestWorker struct {
	workerConfig
	useSyscallMonitor bool
	useNetworkProxy   bool
}

var _ packageWorker = &attestWorker{}

func (w *attestWorker) Setup(ctx context.Context) {
	w.execService.Warmup(ctx)
}

func (w *attestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	if len(p.Artifacts) > 0 && len(p.Artifacts) != len(p.Versions) {
		log.Fatalf("Provided artifact slice does not match versions: %s", p.Name)
	}
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
			ID:                w.runID,
			UseSyscallMonitor: w.useSyscallMonitor,
			UseNetworkProxy:   w.useNetworkProxy,
		}
		verdict, err := w.execService.RebuildPackage(ctx, req)
		if err != nil {
			out <- schema.Verdict{
				Target: rebuild.Target{
					Ecosystem: rebuild.Ecosystem(p.Ecosystem),
					Package:   p.Name,
					Version:   v,
					Artifact:  artifact,
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
}

var _ packageWorker = &smoketestWorker{}

func (w *smoketestWorker) Setup(ctx context.Context) {
	w.execService.Warmup(ctx)
}

func (w *smoketestWorker) ProcessOne(ctx context.Context, p benchmark.Package, out chan schema.Verdict) {
	<-w.limiters[p.Ecosystem]
	req := schema.SmoketestRequest{
		Ecosystem: rebuild.Ecosystem(p.Ecosystem),
		Package:   p.Name,
		Versions:  p.Versions,
		ID:        w.runID,
	}
	resp, err := w.execService.SmoketestPackage(ctx, req)
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
	for _, verdict := range resp.Verdicts {
		out <- verdict
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

type RunBenchOpts struct {
	Mode              schema.ExecutionMode
	RunID             string
	MaxConcurrency    int
	UseSyscallMonitor bool
	UseNetworkProxy   bool
	ExecService       ExecutionService
}

func RunBench(ctx context.Context, set benchmark.PackageSet, opts RunBenchOpts) (<-chan schema.Verdict, error) {
	if opts.RunID == "" {
		return nil, errors.New("opts.RunID must be set")
	}
	if opts.ExecService == nil {
		return nil, errors.New("opts.ExecService must be set")
	}
	if (opts.UseNetworkProxy || opts.UseSyscallMonitor) && opts.Mode != schema.AttestMode {
		return nil, errors.New("cannot enable network proxy or syscall monitor for non-attest mode")
	}
	conf := workerConfig{
		execService: opts.ExecService,
		limiters:    defaultLimiters(),
		runID:       opts.RunID,
	}
	ex := executor{Concurrency: opts.MaxConcurrency}
	switch opts.Mode {
	case schema.SmoketestMode:
		ex.Worker = &smoketestWorker{
			workerConfig: conf,
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
	verdicts := make(chan schema.Verdict)

	go ex.Process(ctx, verdicts, set.Packages)

	return verdicts, nil
}

func RunBenchAsync(ctx context.Context, set benchmark.PackageSet, mode schema.ExecutionMode, apiURL *url.URL, runID string, queue taskqueue.Queue) error {
	if apiURL == nil {
		return errors.New("apiURL must be provided for RunBenchAsync")
	}
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
