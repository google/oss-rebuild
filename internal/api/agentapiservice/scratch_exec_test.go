// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/api/scratchworkerservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeWorker mimics the worker's /exec/start contract: parses the
// form, records the request, returns {}. Tests can swap in a 500 to
// exercise the POST-failure path.
type fakeWorker struct {
	srv        *httptest.Server
	received   *scratchworkerservice.StartRequest
	respStatus int
}

func newFakeWorker(t *testing.T) *fakeWorker {
	t.Helper()
	fw := &fakeWorker{respStatus: http.StatusOK}
	fw.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exec/start" {
			http.NotFound(rw, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		var req scratchworkerservice.StartRequest
		if err := form.Unmarshal(r.Form, &req); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		fw.received = &req
		if fw.respStatus != http.StatusOK {
			http.Error(rw, "boom", fw.respStatus)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte("{}"))
	}))
	t.Cleanup(fw.srv.Close)
	return fw
}

func (f *fakeWorker) dialer() WorkerDialer {
	return func(_ schema.Scratch) (httpx.BasicClient, *url.URL, error) {
		u, _ := url.Parse(f.srv.URL)
		return f.srv.Client(), u, nil
	}
}

// fixture mints a ready scratch and returns wired deps + the fake worker.
// The Get deps leave Syncer nil, exercising the no-sync code path.
func fixture(t *testing.T, opID string, fw *fakeWorker) (*ScratchExecCreateDeps, *ScratchExecGetDeps, db.Scratch, db.ScratchExecs) {
	t.Helper()
	scratches := db.NewMemoryScratch()
	if err := scratches.Insert(context.Background(), schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, VMName: "vm-1", InternalIP: "10.0.0.1",
		ObliviousID: "obid-s-1",
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	execs := db.NewMemoryScratchExecs()
	createDeps := &ScratchExecCreateDeps{
		Scratches:    scratches,
		Execs:        execs,
		WorkerDialer: fw.dialer(),
		OutputBucket: "test-output",
		IDGen:        func() string { return opID },
	}
	getDeps := &ScratchExecGetDeps{Scratches: scratches, Execs: execs}
	return createDeps, getDeps, scratches, execs
}

func TestScratchExecCreate_HappyPath(t *testing.T) {
	const opID = "op-1"
	fw := newFakeWorker(t)
	createDeps, _, _, execs := fixture(t, opID, fw)

	req := schema.ScratchExecRequest{
		ScratchID:      "s-1",
		Cmd:            []string{"sh", "-c", "echo hi"},
		Cwd:            "/work",
		Env:            map[string]string{"FOO": "bar"},
		StdinB64:       "aGk=",
		TimeoutSeconds: 30,
	}
	op, err := ScratchExecCreate(context.Background(), req, createDeps)
	if err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}
	if op.ID != opID {
		t.Errorf("ID = %q; want %q", op.ID, opID)
	}
	if op.Done || op.Error != nil {
		t.Errorf("op = %+v; want Done:false Error:nil", op)
	}

	stored, err := execs.Get(context.Background(), opID)
	if err != nil {
		t.Fatalf("execs.Get: %v", err)
	}
	if stored.State != schema.ScratchExecPending {
		t.Errorf("persisted State = %q; want pending", stored.State)
	}

	if fw.received == nil {
		t.Fatalf("worker did not receive a request")
	}
	want := scratchworkerservice.StartRequest{
		OpID:           opID,
		ScratchID:      "s-1",
		Cmd:            []string{"sh", "-c", "echo hi"},
		Cwd:            "/work",
		Env:            map[string]string{"FOO": "bar"},
		StdinB64:       "aGk=",
		TimeoutSeconds: 30,
	}
	if diff := cmp.Diff(want, *fw.received); diff != "" {
		t.Errorf("worker request mismatch (-want +got):\n%s", diff)
	}
	if op.Result == nil || op.Result.OutURI == "" {
		t.Errorf("op.Result.OutURI unset; want pre-populated")
	}
	wantURI := "gs://test-output/obid-s-1/" + opID + "/out"
	if op.Result != nil && op.Result.OutURI != wantURI {
		t.Errorf("OutURI = %q; want %q", op.Result.OutURI, wantURI)
	}
}

func TestScratchExecCreate_NoObliviousIDFails(t *testing.T) {
	const opID = "op-no-obid"
	fw := newFakeWorker(t)
	scratches := db.NewMemoryScratch()
	if err := scratches.Insert(context.Background(), schema.Scratch{
		ID: "s-1", State: schema.ScratchReady,
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	deps := &ScratchExecCreateDeps{
		Scratches:    scratches,
		Execs:        db.NewMemoryScratchExecs(),
		WorkerDialer: fw.dialer(),
		OutputBucket: "test-output",
		IDGen:        func() string { return opID },
	}
	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, deps)
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %s; want Internal. err=%v", status.Code(err), err)
	}
}

func TestScratchExecCreate_NotFound(t *testing.T) {
	fw := newFakeWorker(t)
	createDeps, _, _, _ := fixture(t, "op-x", fw)
	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "missing", Cmd: []string{"true"},
	}, createDeps)
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %s; want NotFound. err=%v", status.Code(err), err)
	}
}

func TestScratchExecCreate_NotReady(t *testing.T) {
	fw := newFakeWorker(t)
	createDeps, _, scratches, _ := fixture(t, "op-x", fw)
	if err := scratches.UpdateState(context.Background(), "s-1", schema.ScratchStarting); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps)
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code = %s; want FailedPrecondition. err=%v", status.Code(err), err)
	}
}

func TestScratchExecCreate_WorkerPostFailureFinalizesLost(t *testing.T) {
	const opID = "op-post-fail"
	fw := newFakeWorker(t)
	fw.respStatus = http.StatusInternalServerError
	createDeps, _, _, execs := fixture(t, opID, fw)

	op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps)
	if err != nil {
		t.Fatalf("ScratchExecCreate: %v; want nil (Error returned via op)", err)
	}
	if !op.Done {
		t.Errorf("Done = false; want true (finalized on POST failure)")
	}
	if op.Error == nil || op.Error.Code != int(codes.Unavailable) {
		t.Errorf("op.Error = %+v; want code Unavailable", op.Error)
	}

	stored, err := execs.Get(context.Background(), opID)
	if err != nil {
		t.Fatalf("execs.Get: %v", err)
	}
	if stored.State != schema.ScratchExecLost {
		t.Errorf("persisted State = %q; want lost", stored.State)
	}
	if stored.Error == nil || stored.Error.Code != int(codes.Unavailable) {
		t.Errorf("persisted Error = %+v; want code Unavailable", stored.Error)
	}
}

func TestScratchExecCreate_DialerFailureReturnsAPIError(t *testing.T) {
	const opID = "op-dialer-fail"
	scratches := db.NewMemoryScratch()
	_ = scratches.Insert(context.Background(), schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, ObliviousID: "obid-s-1",
	})
	execs := db.NewMemoryScratchExecs()
	deps := &ScratchExecCreateDeps{
		Scratches: scratches,
		Execs:     execs,
		WorkerDialer: func(_ schema.Scratch) (httpx.BasicClient, *url.URL, error) {
			return nil, nil, errors.New("dialer boom")
		},
		OutputBucket: "test-output",
		IDGen:        func() string { return opID },
	}
	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, deps)
	if status.Code(err) != codes.Unavailable {
		t.Errorf("code = %s; want Unavailable. err=%v", status.Code(err), err)
	}
	// No exec record should exist: the dialer runs before Insert.
	if _, gerr := execs.Get(context.Background(), opID); !errors.Is(gerr, db.ErrNotFound) {
		t.Errorf("exec exists after dialer failure; want no record (gerr=%v)", gerr)
	}
}

// faultingExecs wraps db.ScratchExecs and forces Finalize to fail with the
// configured error. Insert and Get pass through unchanged.
type faultingExecs struct {
	db.ScratchExecs
	finalizeErr error
}

func (f *faultingExecs) Finalize(ctx context.Context, e schema.ScratchExec) (schema.ScratchExec, error) {
	if f.finalizeErr != nil {
		return schema.ScratchExec{}, f.finalizeErr
	}
	return f.ScratchExecs.Finalize(ctx, e)
}

// TestScratchExecCreate_FinalizePersistFailureSurfacesAPIError covers the
// invariant that storage and the returned Operation must agree: if finalize
// cannot persist the Lost transition, we return an API error rather than
// project the unpersisted in-memory state (which would disagree with what a
// subsequent Get reads from storage).
func TestScratchExecCreate_FinalizePersistFailureSurfacesAPIError(t *testing.T) {
	const opID = "op-finalize-fail"
	fw := newFakeWorker(t)
	fw.respStatus = http.StatusInternalServerError // force the worker-dispatch failure path

	scratches := db.NewMemoryScratch()
	_ = scratches.Insert(context.Background(), schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, ObliviousID: "obid-s-1",
	})
	execs := &faultingExecs{ScratchExecs: db.NewMemoryScratchExecs(), finalizeErr: errors.New("firestore boom")}
	deps := &ScratchExecCreateDeps{
		Scratches:    scratches,
		Execs:        execs,
		WorkerDialer: fw.dialer(),
		OutputBucket: "test-output",
		IDGen:        func() string { return opID },
	}

	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, deps)
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %s; want Internal. err=%v", status.Code(err), err)
	}

	// Underlying record stays Pending because the finalize Update was rejected;
	// the reaper is the eventual cleanup path.
	stored, gerr := execs.Get(context.Background(), opID)
	if gerr != nil {
		t.Fatalf("execs.Get: %v", gerr)
	}
	if stored.State != schema.ScratchExecPending {
		t.Errorf("persisted State = %q; want still pending (Update failed)", stored.State)
	}
}

// A request without TimeoutSeconds gets the configured default stamped on
// the record and forwarded to the worker, so every exec is bounded.
func TestScratchExecCreate_TimeoutDefaultedAndStamped(t *testing.T) {
	const opID = "op-default-timeout"
	fw := newFakeWorker(t)
	createDeps, _, _, execs := fixture(t, opID, fw)
	createDeps.OpTimeout = time.Hour

	if _, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps); err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}
	stored, err := execs.Get(context.Background(), opID)
	if err != nil {
		t.Fatalf("execs.Get: %v", err)
	}
	if want := 3600; stored.TimeoutSeconds != want {
		t.Errorf("persisted TimeoutSeconds = %d; want %d", stored.TimeoutSeconds, want)
	}
	if fw.received == nil || fw.received.TimeoutSeconds != 3600 {
		t.Errorf("worker TimeoutSeconds = %+v; want 3600", fw.received)
	}
}

func TestScratchExecCreate_TimeoutOverMaxRejected(t *testing.T) {
	const opID = "op-over-max"
	fw := newFakeWorker(t)
	createDeps, _, _, execs := fixture(t, opID, fw)
	createDeps.OpTimeout = time.Minute

	_, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"}, TimeoutSeconds: 61,
	}, createDeps)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %s; want InvalidArgument. err=%v", status.Code(err), err)
	}
	// Rejection happens before Insert: no dangling record.
	if _, gerr := execs.Get(context.Background(), opID); !errors.Is(gerr, db.ErrNotFound) {
		t.Errorf("exec exists after rejection; want no record (gerr=%v)", gerr)
	}
}

func TestScratchExecCreate_BumpsLastUsedOnSuccess(t *testing.T) {
	const opID = "op-bump"
	fw := newFakeWorker(t)
	createDeps, _, scratches, _ := fixture(t, opID, fw)
	before, _ := scratches.Get(context.Background(), "s-1")

	if _, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps); err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}
	after, _ := scratches.Get(context.Background(), "s-1")
	if !after.LastUsed.After(before.LastUsed) {
		t.Errorf("LastUsed before=%v after=%v; expected bump", before.LastUsed, after.LastUsed)
	}
}

// stubOKClient answers every request with 200 {} in-process: a real
// httptest server's transport goroutines would deadlock synctest bubble exit.
type stubOKClient struct{}

func (stubOKClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{}")),
	}, nil
}

func stubDialer() WorkerDialer {
	return func(schema.Scratch) (httpx.BasicClient, *url.URL, error) {
		u, _ := url.Parse("http://worker.invalid")
		return stubOKClient{}, u, nil
	}
}

type syncResult struct {
	exec schema.ScratchExec
	err  error
}

// seqSyncer returns its queued results in call order; the last repeats.
type seqSyncer struct {
	calls   int
	results []syncResult
}

func (s *seqSyncer) Sync(_ context.Context, exec schema.ScratchExec, _ schema.Scratch) (schema.ScratchExec, error) {
	i := min(s.calls, len(s.results)-1)
	s.calls++
	if r := s.results[i]; r.err != nil {
		return exec, r.err
	} else {
		return r.exec, nil
	}
}

// waitFixture mints a ready scratch and create deps wired with the
// network-free stub dialer, suitable for use inside a synctest bubble.
func waitFixture(t *testing.T, opID string, sync Syncer) (*ScratchExecCreateDeps, db.ScratchExecs) {
	t.Helper()
	scratches := db.NewMemoryScratch()
	if err := scratches.Insert(context.Background(), schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, VMName: "vm-1", InternalIP: "10.0.0.1",
		ObliviousID: "obid-s-1",
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	execs := db.NewMemoryScratchExecs()
	return &ScratchExecCreateDeps{
		Scratches:    scratches,
		Execs:        execs,
		WorkerDialer: stubDialer(),
		OutputBucket: "test-output",
		IDGen:        func() string { return opID },
		Syncer:       sync,
	}, execs
}

func TestScratchExecCreate_OptimisticWaitReturnsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const opID = "op-wait-done"
		sync := &seqSyncer{results: []syncResult{
			{exec: schema.ScratchExec{ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted}},
		}}
		deps, _ := waitFixture(t, opID, sync)

		start := time.Now()
		op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
			ScratchID: "s-1", Cmd: []string{"true"}, WaitSeconds: 5,
		}, deps)
		if err != nil {
			t.Fatalf("ScratchExecCreate: %v", err)
		}
		if !op.Done || op.Error != nil {
			t.Errorf("op = %+v; want Done:true Error:nil", op)
		}
		if sync.calls != 1 {
			t.Errorf("Sync calls = %d; want 1", sync.calls)
		}
		// Fast commands return on the first probe, not the full budget.
		if elapsed := time.Since(start); elapsed != optimisticPollInterval {
			t.Errorf("elapsed = %v; want %v (first poll)", elapsed, optimisticPollInterval)
		}
	})
}

// Sync errors during the optimistic wait are retried rather than finalized:
// the wait is best-effort, so a transient sync failure shouldn't finalize an
// op that a later poll could observe normally.
func TestScratchExecCreate_OptimisticWaitToleratesEarlySyncError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const opID = "op-wait-race"
		sync := &seqSyncer{results: []syncResult{
			{err: errors.New("unknown op")},
			{exec: schema.ScratchExec{ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted}},
		}}
		deps, execs := waitFixture(t, opID, sync)

		op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
			ScratchID: "s-1", Cmd: []string{"true"}, WaitSeconds: 5,
		}, deps)
		if err != nil {
			t.Fatalf("ScratchExecCreate: %v", err)
		}
		if !op.Done || op.Error != nil {
			t.Errorf("op = %+v; want Done:true Error:nil", op)
		}
		if sync.calls != 2 {
			t.Errorf("Sync calls = %d; want 2", sync.calls)
		}
		// The error must not have triggered a Lost finalization.
		stored, gerr := execs.Get(context.Background(), opID)
		if gerr != nil {
			t.Fatalf("execs.Get: %v", gerr)
		}
		if stored.State != schema.ScratchExecPending {
			t.Errorf("persisted State = %q; want pending (fake syncer doesn't persist; create must not finalize)", stored.State)
		}
	})
}

func TestScratchExecCreate_OptimisticWaitExpiresPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const opID = "op-wait-expire"
		sync := &seqSyncer{results: []syncResult{
			{exec: schema.ScratchExec{ID: opID, ScratchID: "s-1", State: schema.ScratchExecPending}},
		}}
		deps, _ := waitFixture(t, opID, sync)

		start := time.Now()
		op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
			ScratchID: "s-1", Cmd: []string{"sleep", "999"}, WaitSeconds: 2,
		}, deps)
		if err != nil {
			t.Fatalf("ScratchExecCreate: %v", err)
		}
		if op.Done || op.Error != nil {
			t.Errorf("op = %+v; want Done:false Error:nil (still pending)", op)
		}
		if sync.calls == 0 {
			t.Errorf("Sync calls = 0; want > 0")
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("elapsed = %v; want 2s (full wait budget)", elapsed)
		}
	})
}

func TestScratchExecCreate_OptimisticWaitClampedToMax(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const opID = "op-wait-clamp"
		sync := &seqSyncer{results: []syncResult{
			{exec: schema.ScratchExec{ID: opID, ScratchID: "s-1", State: schema.ScratchExecPending}},
		}}
		deps, _ := waitFixture(t, opID, sync)

		start := time.Now()
		op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
			ScratchID: "s-1", Cmd: []string{"sleep", "999"}, WaitSeconds: 3600,
		}, deps)
		if err != nil {
			t.Fatalf("ScratchExecCreate: %v", err)
		}
		if op.Done {
			t.Errorf("Done = true; want false (still pending)")
		}
		if elapsed := time.Since(start); elapsed != optimisticWaitMax {
			t.Errorf("elapsed = %v; want %v (clamped)", elapsed, optimisticWaitMax)
		}
	})
}

func TestScratchExecCreate_NoWaitSkipsSyncer(t *testing.T) {
	const opID = "op-no-wait"
	sync := &seqSyncer{results: []syncResult{
		{exec: schema.ScratchExec{ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted}},
	}}
	deps, _ := waitFixture(t, opID, sync)

	op, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, deps)
	if err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}
	if op.Done {
		t.Errorf("Done = true; want false (no wait requested)")
	}
	if sync.calls != 0 {
		t.Errorf("Sync calls = %d; want 0", sync.calls)
	}
}

func TestScratchExecGet_RoundTrip(t *testing.T) {
	const opID = "op-2"
	fw := newFakeWorker(t)
	createDeps, getDeps, _, execs := fixture(t, opID, fw)

	if _, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps); err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}

	op, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps)
	if err != nil {
		t.Fatalf("ScratchExecGet(pending): %v", err)
	}
	if op.Done {
		t.Errorf("pending op Done = true; want false")
	}

	// Simulate a sync writing a terminal Completed record.
	final, _ := execs.Get(context.Background(), opID)
	final.State = schema.ScratchExecCompleted
	final.ExitCode = 0
	if _, err := execs.Finalize(context.Background(), final); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	op, err = ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps)
	if err != nil {
		t.Fatalf("ScratchExecGet(final): %v", err)
	}
	if !op.Done || op.Result == nil {
		t.Errorf("final op = %+v; want Done:true Result:set", op)
	}
	if op.Error != nil {
		t.Errorf("Error = %+v; want nil (Completed)", op.Error)
	}
}

// fakeSyncer captures Sync calls and returns a canned exec.
type fakeSyncer struct {
	calls   int
	returns schema.ScratchExec
	err     error
}

func (f *fakeSyncer) Sync(_ context.Context, _ schema.ScratchExec, _ schema.Scratch) (schema.ScratchExec, error) {
	f.calls++
	return f.returns, f.err
}

func TestScratchExecGet_PendingTriggersSync(t *testing.T) {
	const opID = "op-sync"
	fw := newFakeWorker(t)
	createDeps, getDeps, _, _ := fixture(t, opID, fw)

	if _, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps); err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}

	// Inject a fake Syncer that flips the exec to Completed.
	sync := &fakeSyncer{returns: schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted,
	}}
	getDeps.Syncer = sync

	op, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps)
	if err != nil {
		t.Fatalf("ScratchExecGet: %v", err)
	}
	if sync.calls != 1 {
		t.Errorf("Sync calls = %d; want 1", sync.calls)
	}
	if !op.Done {
		t.Errorf("Done = false; want true (Syncer returned terminal)")
	}
}

// The poll that observes the Pending→terminal transition bumps LastUsed so
// the agent gets a fresh idle window to act on the result of a long exec.
func TestScratchExecGet_CompletionBumpsLastUsed(t *testing.T) {
	const opID = "op-done-bump"
	fw := newFakeWorker(t)
	_, getDeps, scratches, execs := fixture(t, opID, fw)
	if err := execs.Insert(context.Background(), schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecPending,
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	getDeps.Syncer = &fakeSyncer{returns: schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted,
	}}
	before, _ := scratches.Get(context.Background(), "s-1")

	if _, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps); err != nil {
		t.Fatalf("ScratchExecGet: %v", err)
	}
	after, _ := scratches.Get(context.Background(), "s-1")
	if !after.LastUsed.After(before.LastUsed) {
		t.Errorf("LastUsed before=%v after=%v; expected bump on completion", before.LastUsed, after.LastUsed)
	}
}

// A poll that finds the exec still running writes nothing to the scratch:
// in-flight liveness is the reaper's busy exemption, not LastUsed churn.
func TestScratchExecGet_PendingPollDoesNotBumpLastUsed(t *testing.T) {
	const opID = "op-still-running"
	fw := newFakeWorker(t)
	_, getDeps, scratches, execs := fixture(t, opID, fw)
	if err := execs.Insert(context.Background(), schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecPending,
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	getDeps.Syncer = &fakeSyncer{returns: schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecPending,
	}}
	before, _ := scratches.Get(context.Background(), "s-1")

	if _, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps); err != nil {
		t.Fatalf("ScratchExecGet: %v", err)
	}
	after, _ := scratches.Get(context.Background(), "s-1")
	if !after.LastUsed.Equal(before.LastUsed) {
		t.Errorf("LastUsed before=%v after=%v; want unchanged on pending poll", before.LastUsed, after.LastUsed)
	}
}

func TestScratchExecGet_TerminalRecordSkipsSync(t *testing.T) {
	const opID = "op-already-done"
	fw := newFakeWorker(t)
	_, getDeps, _, execs := fixture(t, opID, fw)
	if err := execs.Insert(context.Background(), schema.ScratchExec{
		ID: opID, ScratchID: "s-1", State: schema.ScratchExecCompleted,
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	sync := &fakeSyncer{}
	getDeps.Syncer = sync

	op, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps)
	if err != nil {
		t.Fatalf("ScratchExecGet: %v", err)
	}
	if sync.calls != 0 {
		t.Errorf("Sync calls = %d; want 0 (already terminal)", sync.calls)
	}
	if !op.Done {
		t.Errorf("Done = false; want true")
	}
}

// Under the last-error-is-final policy, a sync error finalizes the exec as
// Lost on the first failure rather than re-polling. Verifies the resulting
// Operation is Done with an Unavailable error and the record on storage agrees.
func TestScratchExecGet_SyncErrorFinalizesLost(t *testing.T) {
	const opID = "op-sync-err"
	fw := newFakeWorker(t)
	createDeps, getDeps, _, execs := fixture(t, opID, fw)
	if _, err := ScratchExecCreate(context.Background(), schema.ScratchExecRequest{
		ScratchID: "s-1", Cmd: []string{"true"},
	}, createDeps); err != nil {
		t.Fatalf("ScratchExecCreate: %v", err)
	}
	getDeps.Syncer = &fakeSyncer{err: errors.New("worker unreachable")}

	op, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: opID}, getDeps)
	if err != nil {
		t.Fatalf("ScratchExecGet: %v", err)
	}
	if !op.Done || op.Error == nil || op.Error.Code != int(codes.Unavailable) {
		t.Errorf("op = %+v; want Done:true Error.Code:Unavailable", op)
	}
	stored, gerr := execs.Get(context.Background(), opID)
	if gerr != nil {
		t.Fatalf("execs.Get: %v", gerr)
	}
	if stored.State != schema.ScratchExecLost {
		t.Errorf("persisted State = %q; want lost", stored.State)
	}
}

func TestScratchExecGet_NotFound(t *testing.T) {
	deps := &ScratchExecGetDeps{Execs: db.NewMemoryScratchExecs()}
	_, err := ScratchExecGet(context.Background(), schema.GetOperationRequest{ID: "missing"}, deps)
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %s; want NotFound. err=%v", status.Code(err), err)
	}
}

func TestProjectScratchExec_StateAndErrorToOperationError(t *testing.T) {
	cases := []struct {
		name     string
		state    schema.ScratchExecState
		errSt    *schema.Status
		wantCode int
		wantNil  bool
	}{
		{"completed → no error", schema.ScratchExecCompleted, nil, 0, true},
		{"timed_out/deadline_exceeded", schema.ScratchExecTimedOut,
			&schema.Status{Code: int(codes.DeadlineExceeded), Message: "timed out"},
			int(codes.DeadlineExceeded), false},
		{"lost/unavailable", schema.ScratchExecLost,
			&schema.Status{Code: int(codes.Unavailable), Message: "worker gone"},
			int(codes.Unavailable), false},
		{"lost/internal", schema.ScratchExecLost,
			&schema.Status{Code: int(codes.Internal), Message: "boom"},
			int(codes.Internal), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := ProjectScratchExec(schema.ScratchExec{ID: "x", State: tc.state, Error: tc.errSt})
			if tc.wantNil {
				if op.Error != nil {
					t.Errorf("Error = %+v; want nil", op.Error)
				}
				return
			}
			if op.Error == nil || op.Error.Code != tc.wantCode {
				t.Errorf("Error = %+v; want code %d", op.Error, tc.wantCode)
			}
		})
	}
}

func TestProjectScratchExec_PendingNotDone(t *testing.T) {
	op := ProjectScratchExec(schema.ScratchExec{ID: "x", State: schema.ScratchExecPending})
	if op.Done {
		t.Errorf("Done = true; want false (pending)")
	}
	if op.Error != nil {
		t.Errorf("Error = %+v; want nil", op.Error)
	}
}

// TestProjectScratchExec_ResultAndErrorCoexist verifies the documented contract
// that Result is always populated (snapshot of observable state) and Error is
// added on top for terminal-error records. Partial OutURI remains accessible
// via Result.
func TestProjectScratchExec_ResultAndErrorCoexist(t *testing.T) {
	op := ProjectScratchExec(schema.ScratchExec{
		ID:        "x",
		ScratchID: "s",
		OutURI:    "gs://bkt/obid/x/out", // partial output captured before failure
		State:     schema.ScratchExecLost,
		Error:     &schema.Status{Code: int(codes.Unavailable), Message: "worker gone"},
	})
	if op.Result == nil || op.Result.OutURI != "gs://bkt/obid/x/out" {
		t.Errorf("Result = %+v; want partial OutURI populated alongside Error", op.Result)
	}
	if op.Error == nil || op.Error.Code != int(codes.Unavailable) {
		t.Errorf("Error = %+v; want Unavailable", op.Error)
	}
}

// Compile-time assertion that ProjectScratchExec returns the expected
// generic Operation type (avoids an unused longrunning import warning).
var _ = longrunning.Operation[schema.ScratchExecResult]{}
