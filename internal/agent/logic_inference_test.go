// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

func TestToolchainStrategy(t *testing.T) {
	loc := rebuild.Location{Repo: "https://github.com/example/pkg", Ref: "abc123", Dir: "packages/pkg"}
	published := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	env := rebuild.BuildEnv{TimewarpHost: "localhost:8080"}
	// holds reports whether the script holds want, where an empty want means an empty script.
	holds := func(script, want string) bool {
		return script == want || (want != "" && strings.Contains(script, want))
	}
	for eco, want := range map[rebuild.Ecosystem]struct{ deps, build string }{
		rebuild.NPM:      {"node-v24.20.0", "set registry http://npm:2023-01-01T12:00:00Z@localhost:8080"},
		rebuild.PyPI:     {"venv /deps", "PIP_INDEX_URL=http://pypi:2023-01-01T12:00:00Z@localhost:8080/simple"},
		rebuild.CratesIO: {"rustup-init", ""},
		rebuild.Maven:    {"", ""},
	} {
		t.Run(string(eco), func(t *testing.T) {
			target := rebuild.Target{Ecosystem: eco, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"}
			inst, err := toolchainStrategy(eco, loc, published).GenerateFor(target, env)
			if err != nil {
				t.Fatalf("GenerateFor: %v", err)
			}
			if inst.Source == "" || cmp.Diff(loc, inst.Location) != "" {
				t.Errorf("source = %q at %+v, want a checkout of %+v", inst.Source, inst.Location, loc)
			}
			if !holds(inst.Deps, want.deps) {
				t.Errorf("deps = %q, want %q", inst.Deps, want.deps)
			}
			if !holds(inst.Build, want.build) {
				t.Errorf("build = %q, want only the registry pin %q", inst.Build, want.build)
			}
			if inst.OutputPath != "packages/pkg/pkg-1.0.0.tgz" {
				t.Errorf("output = %q", inst.OutputPath)
			}
		})
	}
	target := rebuild.Target{Ecosystem: rebuild.NPM, Package: "pkg", Version: "1.0.0", Artifact: "pkg-1.0.0.tgz"}
	if inst, err := toolchainStrategy(rebuild.NPM, loc, time.Time{}).GenerateFor(target, env); err != nil || inst.Build != "" {
		t.Errorf("without a publish time: build = %q, err = %v, want no build", inst.Build, err)
	}
}

func TestToolchainFallbackNeedsALocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail rebuild.InferenceErrorDetail
		want   bool
	}{
		{"Located", rebuild.InferenceErrorDetail{Location: rebuild.Location{Repo: "https://github.com/example/pkg", Ref: "abc123"}}, true},
		{"KindOnly", rebuild.InferenceErrorDetail{Kind: rebuild.FailureOversize}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &defaultAgent{deps: &AgentDeps{}}
			s, err := a.toolchainFallback(api.AsStatus(codes.Internal, errors.New("no build inferred"), api.AsDetail(tc.detail)))
			if got := s != nil && err == nil; got != tc.want {
				t.Errorf("toolchainFallback() = %v, %v, want a strategy: %t", s, err, tc.want)
			}
		})
	}
}

func TestHistoryContextNamesInferenceFailure(t *testing.T) {
	a := &defaultAgent{deps: &AgentDeps{}, inferenceFailure: "rust version unsupported in MUSL builds"}
	prompt := strings.Join(a.historyContext(nil), "\n")
	if !strings.Contains(prompt, "## Inference") || !strings.Contains(prompt, "rust version unsupported in MUSL builds") {
		t.Errorf("prompt lacks the inference section:\n%s", prompt)
	}
	if strings.Contains(strings.Join((&defaultAgent{deps: &AgentDeps{}}).historyContext(nil), "\n"), "## Inference") {
		t.Error("inference section present without a failure")
	}
}

// hungClient ignores its request's context, as a history scan does.
type hungClient struct{}

func (hungClient) Do(*http.Request) (*http.Response, error) { select {} }

func TestProposeAbandonsHungInference(t *testing.T) {
	a := NewDefaultAgent(testTarget, &AgentDeps{RegistryClient: hungClient{}})
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("terminated signal received")
	cancel(cause)
	if _, err := a.Propose(ctx, nil); !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the cancellation cause", err)
	}
}

func TestInferWithRetries(t *testing.T) {
	transient := errors.New("getting AI repo hint: empty response content")
	for _, tc := range []struct {
		name      string
		errs      []error // one per attempt, nil succeeds
		cancel    bool
		delay     time.Duration
		wantCalls int
		wantErr   bool
	}{
		{name: "succeeds after a transient failure", errs: []error{transient, nil}, wantCalls: 2},
		{name: "gives up after the last attempt", errs: []error{transient, transient, transient, nil}, wantCalls: inferenceAttempts, wantErr: true},
		{name: "oversize is not retried", errs: []error{&gitx.RepoTooLargeError{URL: "https://github.com/org/repo", Limit: 1}, nil}, wantCalls: 1, wantErr: true},
		{name: "stops waiting when the context ends", errs: []error{transient, nil}, cancel: true, delay: time.Hour, wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			var calls int
			infer := func(context.Context) (*schema.StrategyOneOf, error) {
				err := tc.errs[calls]
				calls++
				if err != nil {
					return nil, err
				}
				return &schema.StrategyOneOf{}, nil
			}
			_, err := inferWithRetries(ctx, infer, tc.delay)
			if calls != tc.wantCalls || (err != nil) != tc.wantErr {
				t.Errorf("inferWithRetries() made %d calls with err %v, want %d calls and error %v", calls, err, tc.wantCalls, tc.wantErr)
			}
		})
	}
}

func TestWithPanicRecovered(t *testing.T) {
	// A nil pointer dereference, not an explicit panic call.
	crash := func(context.Context) (*schema.StrategyOneOf, error) {
		type candidate struct{ Parts []string }
		var c *candidate
		_ = len(c.Parts)
		return nil, nil
	}
	_, err := withPanicRecovered(context.Background(), crash)
	if err == nil || !strings.Contains(err.Error(), "inference panicked") {
		t.Fatalf("err = %v; want the panic reported", err)
	}
	// A panic is a defect in the session, not a provider condition.
	if llm.IsTransient(err) {
		t.Error("panic error reads as transient")
	}
}
