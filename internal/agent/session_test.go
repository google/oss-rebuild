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
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

type fakeAgent struct {
	strategy *schema.StrategyOneOf
	// usage is the cumulative usage reported by Usage. When proposeUsage is
	// non-zero, usage advances by it on each Propose to emulate token spend.
	usage        schema.TokenUsage
	proposeUsage schema.TokenUsage
	// proposeErr fails every Propose once proposeOK of them have succeeded.
	proposeErr error
	proposeOK  int
	proposes   int
	recorded   []*schema.AgentIteration // every iteration handed to RecordIteration
	canonical  *schema.StrategyOneOf    // when set, what Canonicalize returns with changed=true
	refusal    string                   // if set, Check refuses with it
	refused    *schema.StrategyOneOf    // if set, the only strategy Check refuses
	checks     int
}

func (a *fakeAgent) Propose(context.Context, *ProposeOpts) (*schema.StrategyOneOf, error) {
	a.proposes++
	if a.proposeErr != nil && a.proposes > a.proposeOK {
		return nil, a.proposeErr
	}
	a.usage = a.usage.Add(a.proposeUsage)
	return a.strategy, nil
}

func (a *fakeAgent) Canonicalize(_ context.Context, _ *ProposeOpts, working *schema.StrategyOneOf) (*schema.StrategyOneOf, bool, error) {
	if a.canonical != nil {
		return a.canonical, true, nil
	}
	return working, false, nil
}

func (a *fakeAgent) Check(_ context.Context, _ *ProposeOpts, s *schema.StrategyOneOf) (string, error) {
	a.checks++
	if a.refused != nil && s != a.refused {
		return "", nil
	}
	return a.refusal, nil
}

func (a *fakeAgent) RecordIteration(i *schema.AgentIteration) { a.recorded = append(a.recorded, i) }

func (a *fakeAgent) Usage() schema.TokenUsage { return a.usage }

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

// TestDoIterationAttachesUsageDelta verifies that a GCB-mode iteration charges
// the request only the tokens consumed by its own Propose, computed as the
// delta of the agent's cumulative usage.
func TestDoIterationAttachesUsageDelta(t *testing.T) {
	strategy := testStrategy()
	agent := &fakeAgent{
		strategy: strategy,
		// Pretend a prior iteration already spent some tokens.
		usage:        schema.TokenUsage{Input: 100, Model: "m"},
		proposeUsage: schema.TokenUsage{Input: 10, Output: 5, Model: "m"},
	}
	var got *schema.AgentCreateIterationRequest
	deps := RunSessionDeps{
		IterationStub: func(_ context.Context, req schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
			got = &req
			return &schema.AgentCreateIterationResponse{Iteration: &schema.AgentIteration{Status: schema.AgentIterationStatusFailed}}, nil
		},
	}
	if _, err := doIteration(context.Background(), "sess", 2, agent, deps); err != nil {
		t.Fatalf("doIteration: %v", err)
	}
	if got == nil || got.Usage == nil {
		t.Fatalf("iteration request usage not set: %+v", got)
	}
	want := schema.TokenUsage{Input: 10, Output: 5, Model: "m"}
	if *got.Usage != want {
		t.Errorf("iteration usage = %+v, want %+v", *got.Usage, want)
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
	if confirmed.Strategy != strategy || confirmed.SessionID != "sess" {
		t.Errorf("confirmation request = %+v, want proposed strategy for session sess", confirmed)
	}
	// A verified build of no measurable length still gets the allowance.
	if want := int(confirmationAllowance.Seconds()); confirmed.BuildTimeoutSeconds < want || confirmed.BuildTimeoutSeconds > int(schema.AgentBuildTimeoutMax.Seconds()) {
		t.Errorf("BuildTimeoutSeconds = %d, want within [%d, %d]", confirmed.BuildTimeoutSeconds, want, int(schema.AgentBuildTimeoutMax.Seconds()))
	}
	if iter.ID != "confirm-1" || iter.Status != schema.AgentIterationStatusSuccess {
		t.Errorf("returned iteration = %+v, want the confirmation iteration", iter)
	}
}

func TestConfirmationTimeout(t *testing.T) {
	for _, tc := range []struct {
		name           string
		verified, want time.Duration
	}{
		{"NoVMBuild", 0, schema.AgentBuildTimeoutMax},
		{"QuickVMBuild", 30 * time.Second, confirmationAllowance + time.Minute},
		{"SlowVMBuild", 10 * time.Minute, 30 * time.Minute},
		{"CappedAtTheMax", time.Hour, schema.AgentBuildTimeoutMax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmationTimeout(tc.verified); got != tc.want {
				t.Errorf("confirmationTimeout(%s) = %s, want %s", tc.verified, got, tc.want)
			}
		})
	}
}

func TestRunIterationsProposalFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		agent        *fakeAgent
		wantReason   string
		wantSummary  string // a prefix
		wantProposes int
		wantBuilds   int
	}{
		{"a heuristic failure before any build ends the session", &fakeAgent{proposeErr: errors.New("no valid git ref")}, schema.AgentCompleteReasonFailed, "Inference failed: ", 1, 0},
		{"a transient failure is retried until throttled", &fakeAgent{proposeErr: genai.APIError{Code: 429, Message: "Resource exhausted"}}, schema.AgentCompleteReasonThrottled, "", 3, 0},
		// Once a build has run the proposal comes from the model and may differ
		// next time, so a failure spends only its own iteration.
		{"a failure after a build spends only its iteration", &fakeAgent{strategy: testStrategy(), proposeErr: errors.New("hypothesize: chat error"), proposeOK: 1}, schema.AgentCompleteReasonFailed, "Maximum iterations", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var builds int
			deps := RunSessionDeps{
				IterationStub: func(context.Context, schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
					builds++
					return &schema.AgentCreateIterationResponse{Iteration: &schema.AgentIteration{Status: schema.AgentIterationStatusFailed}}, nil
				},
			}
			got := runIterations(context.Background(), RunSessionReq{SessionID: "sess", MaxIterations: 3}, 0, tc.agent, deps)
			if got.StopReason != tc.wantReason || !strings.HasPrefix(got.Summary, tc.wantSummary) {
				t.Errorf("verdict = %s %q, want %s %q", got.StopReason, got.Summary, tc.wantReason, tc.wantSummary)
			}
			if tc.agent.proposes != tc.wantProposes || builds != tc.wantBuilds {
				t.Errorf("proposes, builds = %d, %d; want %d, %d", tc.agent.proposes, builds, tc.wantProposes, tc.wantBuilds)
			}
		})
	}
}

func TestRunSessionReportsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var got *schema.AgentCompleteRequest
	deps := RunSessionDeps{CompleteStub: func(ctx context.Context, req schema.AgentCompleteRequest) (*schema.AgentCompleteResponse, error) {
		if ctx.Err() != nil {
			t.Errorf("completion context is done: %v", ctx.Err())
		}
		got = &req
		return &schema.AgentCompleteResponse{}, nil
	}}
	// MaxIterations 0 ends the session before any inference runs.
	RunSession(ctx, RunSessionReq{SessionID: "sess"}, deps)
	if got == nil || got.SessionID != "sess" {
		t.Fatalf("completion = %+v, want a report for sess", got)
	}
}

// canonicalStrategy is a distinct ManualStrategy standing in for a
// canonicalized rewrite of testStrategy.
func canonicalStrategy() *schema.StrategyOneOf {
	return new(schema.NewStrategyOneOf(&rebuild.ManualStrategy{
		Location:   rebuild.Location{Repo: "https://github.com/lodash/lodash", Ref: "aa1d7d870d9cf84842ee23ff485fd24abf0ed3d1", Dir: "."},
		Deps:       "npm install -g yarn --registry $REG\nyarn install",
		Build:      "yarn pack",
		OutputPath: "lodash-4.17.21.tgz",
	}))
}

// nthFailExecutor delegates to inner but returns a failing build on the
// failOn-th Start call, so a test can fail just the canonical re-verify.
type nthFailExecutor struct {
	inner  build.Executor
	failOn int
	n      int
}

func (e *nthFailExecutor) Start(ctx context.Context, input rebuild.Input, opts build.Options) (build.Handle, error) {
	e.n++
	if e.n == e.failOn {
		return &fakeHandle{result: build.Result{Error: &scratch.ExitError{Code: 1}}}, nil
	}
	return e.inner.Start(ctx, input, opts)
}
func (e *nthFailExecutor) Status() build.ExecutorStatus    { return e.inner.Status() }
func (e *nthFailExecutor) Close(ctx context.Context) error { return e.inner.Close(ctx) }

func TestDoIterationCanonicalForm(t *testing.T) {
	original, canonical := testStrategy(), canonicalStrategy()
	for _, tc := range []struct {
		name    string
		failOn  int  // the Start call that fails, 0 for none
		refused bool // Check refuses the canonical form
		want    *schema.StrategyOneOf
	}{
		{name: "Adopted", want: canonical},
		{name: "ReverifyFails", failOn: 2, want: original},
		{name: "Refused", refused: true, want: original},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact := testTarball(t)
			runner := testRunner(&nthFailExecutor{inner: &fakeExecutor{artifact: artifact}, failOn: tc.failOn})
			runner.RegistryClient = &fakeRegistry{artifact: artifact}
			a := &fakeAgent{strategy: original, canonical: canonical}
			if tc.refused {
				a.refusal, a.refused = "proposal refused", canonical
			}
			var confirmed *schema.AgentCreateIterationRequest
			deps := RunSessionDeps{
				ScratchRunner: runner,
				IterationStub: func(_ context.Context, req schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
					confirmed = &req
					return &schema.AgentCreateIterationResponse{Iteration: &schema.AgentIteration{ID: "c1", Status: schema.AgentIterationStatusSuccess}}, nil
				},
			}
			if _, err := doIteration(context.Background(), "sess", 2, a, deps); err != nil {
				t.Fatalf("doIteration: %v", err)
			}
			if confirmed == nil || confirmed.Strategy != tc.want {
				t.Errorf("confirmed strategy = %v, want %v", confirmed, tc.want)
			}
		})
	}
}

func TestDoIterationRefusedProposalIsLocalOnly(t *testing.T) {
	artifact := testTarball(t)
	runner := testRunner(&fakeExecutor{artifact: artifact})
	runner.RegistryClient = &fakeRegistry{artifact: artifact}
	deps := RunSessionDeps{
		ScratchRunner: runner,
		IterationStub: func(context.Context, schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
			t.Error("IterationStub called for a refused proposal")
			return nil, errors.New("unexpected call")
		},
	}
	a := &fakeAgent{strategy: testStrategy(), refusal: "proposal refused: the script supplies artifact content"}
	iter, err := doIteration(context.Background(), "sess", 2, a, deps)
	if err != nil {
		t.Fatalf("doIteration: %v", err)
	}
	if a.checks != 1 {
		t.Errorf("Check called %d times, want 1", a.checks)
	}
	if iter.ID != "" || iter.Status != iterationStatusRefused || iter.Result == nil || iter.Result.ErrorMessage != a.refusal {
		t.Errorf("refused iteration = %+v (result %+v)", iter, iter.Result)
	}
}

func TestRunIterationsStopsAfterRepeatedRefusals(t *testing.T) {
	artifact := testTarball(t)
	runner := testRunner(&fakeExecutor{artifact: artifact})
	runner.RegistryClient = &fakeRegistry{artifact: artifact}
	deps := RunSessionDeps{ScratchRunner: runner}
	a := &fakeAgent{strategy: testStrategy(), refusal: "proposal refused"}
	req := RunSessionReq{SessionID: "sess", MaxIterations: 10, InitialIteration: &schema.AgentIteration{Strategy: testStrategy()}}
	done := runIterations(context.Background(), req, 1, a, deps)
	if done.StopReason != schema.AgentCompleteReasonFailed || !strings.Contains(done.Summary, "Refused 3 proposals") {
		t.Errorf("completion = %+v, want FAILED after %d refusals", done, maxRefusals)
	}
	// Every refusal is recorded so the model sees it in the history.
	if len(a.recorded) != maxRefusals {
		t.Errorf("recorded %d iterations, want %d", len(a.recorded), maxRefusals)
	}
	for _, it := range a.recorded {
		if it.Status != iterationStatusRefused || it.Result == nil || it.Result.ErrorMessage != a.refusal {
			t.Errorf("recorded iteration is not a refusal: %+v", it)
		}
	}
}

func TestRunIterationsStopsOnGiveUp(t *testing.T) {
	closest := &schema.AgentCandidate{Path: "/src/dist/x.whl", Entries: 1}
	a := &fakeAgent{proposeErr: gaveUp{Reason: "the wheel ships a coverage file that is not in the source", Closest: closest}}
	req := RunSessionReq{SessionID: "sess", MaxIterations: 5, InitialIteration: &schema.AgentIteration{Strategy: testStrategy()}}
	done := runIterations(context.Background(), req, 1, a, RunSessionDeps{})
	if done.StopReason != schema.AgentCompleteReasonGaveUp || done.Summary != "Gave up: the wheel ships a coverage file that is not in the source" || done.Closest != closest {
		t.Errorf("completion = %+v, want GAVE_UP with the reason and the closest candidate", done)
	}
	if a.proposes != 1 {
		t.Errorf("Propose called %d times, want the one that gave up", a.proposes)
	}
}

func TestRunIterationsStopsAtTheIterationBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		step         time.Duration // fake time each build takes
		wantSummary  string        // prefix
		wantProposes int
	}{
		// Builds start at 0, 4 and 8 minutes. The fourth would start past the budget.
		{"BudgetSpent", 4 * time.Minute, "Iteration budget (10m0s) spent after 3 builds", 3},
		// A first turn slower than slowIteration lifts the budget, and the
		// iteration cap is what ends the session.
		{"SlowFirstTurnLiftsTheBudget", 6 * time.Minute, "Maximum iterations", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every build advances a fake clock by step, so the budget's deadline
			// falls between builds.
			clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			deps := RunSessionDeps{
				IterationStub: func(context.Context, schema.AgentCreateIterationRequest) (*schema.AgentCreateIterationResponse, error) {
					clock = clock.Add(tc.step)
					return &schema.AgentCreateIterationResponse{Iteration: &schema.AgentIteration{Status: schema.AgentIterationStatusFailed}}, nil
				},
				Now: func() time.Time { return clock },
			}
			req := RunSessionReq{SessionID: "sess", MaxIterations: 5, IterationBudget: 10 * time.Minute}
			agent := &fakeAgent{strategy: testStrategy()}
			got := runIterations(context.Background(), req, 0, agent, deps)
			if got.StopReason != schema.AgentCompleteReasonFailed || !strings.HasPrefix(got.Summary, tc.wantSummary) {
				t.Errorf("verdict = %s %q, want FAILED with %q", got.StopReason, got.Summary, tc.wantSummary)
			}
			if agent.proposes != tc.wantProposes {
				t.Errorf("proposes = %d, want %d", agent.proposes, tc.wantProposes)
			}
		})
	}
}
