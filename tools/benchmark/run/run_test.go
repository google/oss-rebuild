// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package run

import (
	"context"
	"testing"
	"time"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/pkg/errors"
)

type queueCall struct {
	URL  string
	Body string
}

type resultWithErrorService struct{}

func (resultWithErrorService) RebuildPackage(_ context.Context, req schema.RebuildPackageRequest) (*schema.Verdict, error) {
	return &schema.Verdict{
		Target:     rebuild.Target{Ecosystem: req.Ecosystem, Package: req.Package, Version: req.Version, Artifact: req.Artifact},
		Provenance: &schema.StrategyProvenance{Inference: &schema.InferenceRun{Version: "test-infer"}},
	}, errors.New("remote operation failed")
}

func (resultWithErrorService) Infer(context.Context, schema.InferenceRequest) (*schema.StrategyOneOf, error) {
	return nil, errors.New("not implemented")
}

func (resultWithErrorService) Warmup(context.Context) {}

func TestAttestWorkerPreservesResultReturnedWithError(t *testing.T) {
	w := attestWorker{workerConfig: workerConfig{
		execService: resultWithErrorService{},
		limiters: map[string]*ratex.BackoffLimiter{
			"cratesio": ratex.NewBackoffLimiter(time.Nanosecond, time.Millisecond),
		},
	}}
	out := make(chan schema.Verdict, 1)
	w.ProcessOne(context.Background(), benchmark.Package{
		Ecosystem: "cratesio",
		Name:      "serde",
		Versions:  []string{"1.0.150"},
	}, out)
	got := <-out
	if got.Provenance == nil || got.Provenance.Inference == nil || got.Provenance.Inference.Version != "test-infer" {
		t.Fatalf("Provenance = %#v, want returned remote provenance", got.Provenance)
	}
	if got.Message != "remote operation failed" {
		t.Fatalf("Message = %q, want %q", got.Message, "remote operation failed")
	}
}

type mockQueue struct {
	calls []queueCall
}

func (q *mockQueue) Add(ctx context.Context, url string, msg api.Input) (*taskspb.Task, error) {
	body, err := form.Marshal(msg)
	if err != nil {
		return nil, errors.Wrap(err, "marshalling message")
	}
	q.calls = append(q.calls, queueCall{url, body.Encode()})
	return &taskspb.Task{}, nil
}

func TestRunBenchAsync(t *testing.T) {
	testCases := []struct {
		name     string
		mode     schema.ExecutionMode
		set      benchmark.PackageSet
		expected []queueCall
	}{
		{
			name: "attest",
			mode: schema.AttestMode,
			set: benchmark.PackageSet{
				Packages: []benchmark.Package{
					{
						Ecosystem: "npm",
						Name:      "package_name",
						Versions:  []string{"1.0.0", "1.1.0"},
						Execution: schema.ExtendedExecution,
						Size:      schema.JumboSize,
					},
				},
			},
			expected: []queueCall{
				{
					"https://example.com/rebuild",
					"ecosystem=npm&executionhint=EXTENDED&id=runid&overwritemode=FORCE&package=package_name&sizehint=JUMBO&usenetworkproxy=true&userepodefinition=true&usesyscallmonitor=true&version=1.0.0",
				},
				{
					"https://example.com/rebuild",
					"ecosystem=npm&executionhint=EXTENDED&id=runid&overwritemode=FORCE&package=package_name&sizehint=JUMBO&usenetworkproxy=true&userepodefinition=true&usesyscallmonitor=true&version=1.1.0",
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			queue := &mockQueue{}
			url := urlx.MustParse("https://example.com")
			if err := RunBenchAsync(context.Background(), tc.set, RunBenchOpts{
				Mode:              tc.mode,
				RunID:             "runid",
				UseSyscallMonitor: true,
				UseNetworkProxy:   true,
				UseRepoDefinition: true,
				OverwriteMode:     schema.OverwriteForce,
			}, url, queue); err != nil {
				t.Error(errors.Wrap(err, "RunBenchAsync"))
			}
			if diff := cmp.Diff(queue.calls, tc.expected); diff != "" {
				t.Errorf("Unexpected calls to queue: got %v, want %v", queue.calls, tc.expected)
			}
		})
	}
}
