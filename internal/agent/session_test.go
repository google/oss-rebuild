// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

type fakeAgent struct {
	strategy *schema.StrategyOneOf
}

func (a *fakeAgent) Propose(context.Context, *ProposeOpts) (*schema.StrategyOneOf, error) {
	return a.strategy, nil
}

func (a *fakeAgent) RecordIteration(*schema.AgentIteration) {}

func TestDoIterationScratchFailureIsLocalOnly(t *testing.T) {
	runner := testRunner(&fakeExecutor{result: build.Result{Error: &scratch.ExitError{Code: 7}}})
	deps := RunSessionDeps{
		ScratchRunner: runner,
		IterationStub: func(context.Context, schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
			t.Error("IterationStub called for a locally-failed scratch attempt")
			return nil, errors.New("unexpected call")
		},
	}
	strategy := testStrategy()
	iter, err := doIteration(context.Background(), "sess", 3, &fakeAgent{strategy: strategy}, deps)
	if err != nil {
		t.Fatalf("doIteration: %v", err)
	}
	if iter.Status != schema.AgentIterationStatusFailed {
		t.Errorf("Status = %q, want %q", iter.Status, schema.AgentIterationStatusFailed)
	}
	if iter.Number != 3 || iter.SessionID != "sess" {
		t.Errorf("iteration identity = (%q, %d), want (sess, 3)", iter.SessionID, iter.Number)
	}
	if iter.ID != "" {
		t.Errorf("ID = %q, want empty (local-only record)", iter.ID)
	}
	if iter.Strategy != strategy {
		t.Errorf("Strategy = %v, want the proposed strategy", iter.Strategy)
	}
	if iter.Result == nil || !strings.Contains(iter.Result.ErrorMessage, "exit code 7") {
		t.Errorf("Result = %+v, want exit code message", iter.Result)
	}
}

// testTarball returns a minimal valid .tgz (one file) usable as both the
// rebuilt artifact and the faked upstream so verification finds a match.
func testTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("module.exports = {}\n")
	if err := tw.WriteHeader(&tar.Header{Name: "package/index.js", Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRegistry serves npm version metadata and the upstream tarball for
// testTarget without network access.
type fakeRegistry struct {
	artifact []byte
}

func (f *fakeRegistry) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.URL.Host == "registry.npmjs.org" {
		body = []byte(`{"dist":{"tarball":"https://registry.example/lodash-4.17.21.tgz"}}`)
	} else {
		body = f.artifact
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

// TestDoIterationScratchSuccessConfirms exercises the confirmation flow: a
// locally-verified scratch build proceeds through the standard iteration
// call, whose server-derived iteration is what the loop consumes.
func TestDoIterationScratchSuccessConfirms(t *testing.T) {
	artifact := testTarball(t)
	runner := testRunner(&fakeExecutor{artifact: artifact})
	runner.RegistryClient = &fakeRegistry{artifact: artifact}
	strategy := testStrategy()
	var confirmed *schema.AgentCreateIterationRequest
	stubIteration := &schema.AgentIteration{
		ID:        "confirm-1",
		SessionID: "sess",
		Number:    2,
		Strategy:  strategy,
		Status:    schema.AgentIterationStatusSuccess,
		Result:    &schema.AgentBuildResult{BuildSuccess: true},
		Created:   time.Now().UTC(),
	}
	deps := RunSessionDeps{
		ScratchRunner: runner,
		IterationStub: func(_ context.Context, req schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
			confirmed = &req
			return &schema.AgentCreateIterationResponse{
				IterationID: stubIteration.ID,
				Iteration:   stubIteration,
			}, nil
		},
	}
	iter, err := doIteration(context.Background(), "sess", 2, &fakeAgent{strategy: strategy}, deps)
	if err != nil {
		t.Fatalf("doIteration: %v", err)
	}
	if confirmed == nil {
		t.Fatal("IterationStub not called for locally-verified success")
	}
	if confirmed.Strategy != strategy || confirmed.IterationNumber != 2 {
		t.Errorf("confirmation request = %+v, want proposed strategy and iteration number 2", confirmed)
	}
	if iter.ID != "confirm-1" || iter.Status != schema.AgentIterationStatusSuccess {
		t.Errorf("returned iteration = %+v, want the confirmation iteration", iter)
	}
}
