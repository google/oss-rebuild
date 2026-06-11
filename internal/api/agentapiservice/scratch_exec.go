// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"bytes"
	"context"
	"log"
	"net/url"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/api/scratchworkerservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// WorkerDialer returns the HTTP client and base URL for the worker on a
// given scratch. Constructed once per request so the broker can mint
// an ID-token client scoped to the per-VM audience.
type WorkerDialer func(scratch schema.Scratch) (httpx.BasicClient, *url.URL, error)

// ScratchExecCreateDeps wires ScratchExecCreate.
type ScratchExecCreateDeps struct {
	Scratches    db.Scratch
	Execs        db.ScratchExecs
	WorkerDialer WorkerDialer
	OutputBucket string
	IDGen        func() string // For op IDs. If nil, defaults to uuid.New()
	OpTimeout    time.Duration // Default and max exec timeout. Zero leaves execs unbounded
}

// ScratchExecGetDeps wires ScratchExecGet.
type ScratchExecGetDeps struct {
	Scratches db.Scratch
	Execs     db.ScratchExecs
	Syncer    Syncer // to sync the op output. Disabled on nil
}

// Syncer abstracts the broker-side fetch-and-persist loop. The
// production impl pulls /exec/op/status and /exec/op/output from the
// worker, overwrites the op's GCS object with the full buffer (when
// new bytes are available), and updates Firestore on the Done
// transition.
type Syncer interface {
	Sync(ctx context.Context, exec schema.ScratchExec, scratch schema.Scratch) (schema.ScratchExec, error)
}

// ProjectScratchExec adapts the stored exec record to its long-running-operation
// view. Result is always populated as a snapshot of observable state (OutURI may
// be partial in Pending and Failed). Error is populated additionally when the exec
// has terminally failed; the two coexist intentionally so callers can read partial
// output captured before the failure.
func ProjectScratchExec(e schema.ScratchExec) longrunning.Operation[schema.ScratchExecResult] {
	op := longrunning.Operation[schema.ScratchExecResult]{
		ID:   e.ID,
		Done: e.State != schema.ScratchExecPending,
		Result: &schema.ScratchExecResult{
			ScratchID:  e.ScratchID,
			ExitCode:   e.ExitCode,
			OutURI:     e.OutURI,
			StartedAt:  e.StartedAt,
			FinishedAt: e.FinishedAt,
		},
	}
	if e.Error != nil {
		op.Error = &longrunning.OperationError{
			Code:    e.Error.Code,
			Message: e.Error.Message,
		}
	}
	return op
}

// ScratchExecCreate starts an exec on the requested scratch. It mints an opaque
// opID, persists a Pending ScratchExec pre-populated with the immutable parts of
// the eventual Result, dispatches the work to the worker, and returns the projected
// operation.
//
// API-error vs Operation-error contract: failures that happen before the exec
// record is durably inserted surface as API status errors (the operation has no
// identity yet). Failures after Insert succeeds become part of the operation's
// state: the exec is finalized as Failed and returned as a successful response
// whose Operation.Error carries the reason. This matches the longrunning idiom
// and matches what Get would return for the same op id.
func ScratchExecCreate(ctx context.Context, req schema.ScratchExecRequest, deps *ScratchExecCreateDeps) (*longrunning.Operation[schema.ScratchExecResult], error) {
	scratch, err := deps.Scratches.Get(ctx, req.ScratchID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", req.ScratchID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}
	if scratch.State != schema.ScratchReady {
		return nil, api.AsStatus(codes.FailedPrecondition, errors.Errorf("scratch %q not ready (state=%s)", req.ScratchID, scratch.State))
	}
	if scratch.ObliviousID == "" {
		return nil, api.AsStatus(codes.Internal, errors.Errorf("scratch %q missing ObliviousID", req.ScratchID))
	}
	// When OpTimeout is configured, every exec gets a worker-enforced bound:
	// the request value, defaulted when omitted and capped at the maximum.
	// The reaper derives the op's hard deadline from the stamped value and
	// leaves unbounded ops' scratches up.
	maxTimeout := int(deps.OpTimeout.Seconds())
	if maxTimeout > 0 && req.TimeoutSeconds > maxTimeout {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Errorf("timeout_seconds %d exceeds maximum %d", req.TimeoutSeconds, maxTimeout))
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = maxTimeout
	}

	// Prepare the worker client before Insert so a dialer failure (auth setup,
	// URL parse) is a request-time error rather than a dangling Pending record.
	client, baseURL, err := deps.WorkerDialer(scratch)
	if err != nil {
		return nil, api.AsStatus(codes.Unavailable, errors.Wrap(err, "worker dialer"))
	}

	opID := mintID(deps.IDGen)
	startedAt := time.Now().UTC()
	exec := schema.ScratchExec{
		ID:             opID,
		ScratchID:      req.ScratchID,
		Cmd:            req.Cmd,
		Cwd:            req.Cwd,
		TimeoutSeconds: timeoutSeconds,
		State:          schema.ScratchExecPending,
		OutURI:         outURIFor(deps.OutputBucket, scratch.ObliviousID, opID),
		StartedAt:      startedAt,
	}
	if err := deps.Execs.Insert(ctx, exec); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "execs insert"))
	}

	// Past this point the operation has identity. Any failure becomes terminal
	// Operation state, not an API error.

	startStub := api.Stub[scratchworkerservice.StartRequest, act.NoOutput](client, baseURL.JoinPath("exec/start"))
	if _, err := startStub(ctx, scratchworkerservice.StartRequest{
		OpID:           opID,
		ScratchID:      req.ScratchID,
		Cmd:            req.Cmd,
		Cwd:            req.Cwd,
		Env:            req.Env,
		StdinB64:       req.StdinB64,
		TimeoutSeconds: timeoutSeconds,
	}); err != nil {
		log.Printf("scratch %q exec %q: worker dispatch: %v", req.ScratchID, opID, err)
		exec, ferr := finalize(ctx, deps.Execs, exec, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: errors.Wrap(err, "worker dispatch").Error(),
		})
		if ferr != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(ferr, "scratch %q exec %q", req.ScratchID, opID))
		}
		op := ProjectScratchExec(exec)
		return &op, nil
	}

	// Bump LastUsed so a future reaper doesn't pick up a freshly-dispatched
	// scratch. Best effort: a transient failure here doesn't block the op.
	if err := deps.Scratches.UpdateLastUsed(ctx, req.ScratchID, time.Now().UTC()); err != nil {
		log.Printf("scratch %q exec %q: update last_used: %v", req.ScratchID, opID, err)
	}

	op := ProjectScratchExec(exec)
	return &op, nil
}

// ScratchExecGet returns the current state of an exec op. If the op is
// pending and a Syncer is configured, ScratchExecGet triggers a sync:
// pulling the worker's latest status + any new output bytes, rolling
// those bytes into the op's GCS object, and (on the Done transition)
// finalizing Firestore and bumping the scratch's LastUsed so the agent
// has a fresh idle window to act on the result.
//
// Sync errors follow the last-error-is-final policy: any failure to reach the
// worker, read its state, or persist the output transitions the exec to Failed
// immediately. We deliberately do not retry transient errors. For in-VPC
// broker→worker traffic the blip rate is low enough that an occasional false
// Failed is the right cost vs. an exec that lingers in Pending indefinitely.
func ScratchExecGet(ctx context.Context, req schema.GetOperationRequest, deps *ScratchExecGetDeps) (*longrunning.Operation[schema.ScratchExecResult], error) {
	exec, err := deps.Execs.Get(ctx, req.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("op %q not found", req.ID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "execs get"))
	}
	if exec.State != schema.ScratchExecPending || deps.Syncer == nil || exec.ScratchID == "" {
		op := ProjectScratchExec(exec)
		return &op, nil
	}

	scratch, err := deps.Scratches.Get(ctx, exec.ScratchID)
	if err != nil {
		log.Printf("exec %q: scratch %q lookup: %v", exec.ID, exec.ScratchID, err)
		exec, ferr := finalize(ctx, deps.Execs, exec, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: errors.Wrap(err, "scratch lookup").Error(),
		})
		if ferr != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(ferr, "exec %q", exec.ID))
		}
		op := ProjectScratchExec(exec)
		return &op, nil
	}
	if scratch.State != schema.ScratchReady {
		log.Printf("exec %q: scratch %q not ready (state=%s)", exec.ID, exec.ScratchID, scratch.State)
		exec, ferr := finalize(ctx, deps.Execs, exec, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: errors.Errorf("scratch not ready (state=%s)", scratch.State).Error(),
		})
		if ferr != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(ferr, "exec %q", exec.ID))
		}
		op := ProjectScratchExec(exec)
		return &op, nil
	}

	synced, err := deps.Syncer.Sync(ctx, exec, scratch)
	if err != nil {
		log.Printf("exec %q: sync: %v", exec.ID, err)
		exec, ferr := finalize(ctx, deps.Execs, exec, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: err.Error(),
		})
		if ferr != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(ferr, "exec %q", exec.ID))
		}
		op := ProjectScratchExec(exec)
		return &op, nil
	}
	// Bump LastUsed when this poll observed the Pending→terminal transition:
	// a long exec finishes with LastUsed still at dispatch time, and without
	// the bump the scratch is reaped before the agent can act on the result.
	// One write per exec; lives here rather than in Syncer.Sync so the
	// reaper's pull-through never extends the life of an abandoned scratch.
	if synced.State != schema.ScratchExecPending {
		if err := deps.Scratches.UpdateLastUsed(ctx, scratch.ID, time.Now().UTC()); err != nil {
			log.Printf("exec %q: update last_used: %v", exec.ID, err)
		}
	}
	op := ProjectScratchExec(synced)
	return &op, nil
}

// gcsSyncer pulls from the worker's /status + /output endpoints and
// overwrites the op's GCS object with the full buffer on every poll
// that has new bytes.
//
// TODO(streaming): the worker JSON-encodes the entire stdout+stderr
// buffer per poll today (see scratchworkerservice.OutputRequest). Once
// act gains a streaming-response model, switch to incremental tail
// fetches + rolling Compose.
type gcsSyncer struct {
	gcs          *storage.Client
	bucket       string
	execs        db.ScratchExecs
	workerDialer WorkerDialer
}

// NewGCSSyncer returns a Syncer that overwrites the op's GCS object
// with the full worker buffer when new bytes are available, and
// finalizes Firestore on the Done transition.
func NewGCSSyncer(gcs *storage.Client, bucket string, execs db.ScratchExecs, wd WorkerDialer) Syncer {
	return &gcsSyncer{gcs: gcs, bucket: bucket, execs: execs, workerDialer: wd}
}

func (s *gcsSyncer) Sync(ctx context.Context, exec schema.ScratchExec, scratch schema.Scratch) (schema.ScratchExec, error) {
	client, baseURL, err := s.workerDialer(scratch)
	if err != nil {
		return exec, errors.Wrap(err, "worker client")
	}

	statusStub := api.Stub[scratchworkerservice.StatusRequest, scratchworkerservice.ExecStatus](client, baseURL.JoinPath("exec/op/status"))
	status, err := statusStub(ctx, scratchworkerservice.StatusRequest{ID: exec.ID})
	if err != nil {
		return exec, errors.Wrap(err, "worker status")
	}

	// Only round-trip the full buffer if the worker has more bytes than GCS
	// does. The status call's TotalBytes is cheap; /output is O(buffer).
	out := s.gcs.Bucket(s.bucket).Object(outObjectFor(scratch.ObliviousID, exec.ID))
	var currentSize int64
	if attrs, err := out.Attrs(ctx); err == nil {
		currentSize = attrs.Size
	} else if !errors.Is(err, storage.ErrObjectNotExist) {
		return exec, errors.Wrap(err, "stat out")
	}

	if status.TotalBytes > currentSize {
		outputStub := api.Stub[scratchworkerservice.OutputRequest, scratchworkerservice.OutputResponse](client, baseURL.JoinPath("exec/op/output"))
		body, err := outputStub(ctx, scratchworkerservice.OutputRequest{ID: exec.ID})
		if err != nil {
			return exec, errors.Wrap(err, "worker output")
		}
		if err := s.writeOut(ctx, scratch.ObliviousID, exec.ID, body.Bytes); err != nil {
			return exec, errors.Wrap(err, "write out")
		}
	}

	if !status.Done {
		return exec, nil
	}

	final := exec
	final.ExitCode = status.ExitCode
	if !status.FinishedAt.IsZero() {
		final.FinishedAt = status.FinishedAt
	} else {
		final.FinishedAt = time.Now().UTC()
	}
	switch {
	case status.TimedOut:
		// Worker-enforced timeout: command was killed at its deadline.
		// We observed the kill exit (typically 124); partial output remains in OutURI.
		final.State = schema.ScratchExecTimedOut
		final.Error = &schema.Status{
			Code:    int(codes.DeadlineExceeded),
			Message: "command exceeded TimeoutSeconds",
		}
	case status.ErrMsg != "":
		// Worker reported an infra failure (spawn failed, stdin decode, ...).
		// We do not have a known exit; treat as Lost.
		final.State = schema.ScratchExecLost
		final.Error = &schema.Status{
			Code:    int(codes.Internal),
			Message: status.ErrMsg,
		}
	default:
		final.State = schema.ScratchExecCompleted
	}
	if err := s.execs.Update(ctx, final); err != nil {
		return exec, errors.Wrap(err, "execs update final")
	}
	return final, nil
}

// finalize transitions exec to Lost with the given status and persists it;
// idempotent against already-terminal records. On persist failure callers must
// surface the error rather than project the unpersisted state, or the API
// response will disagree with what subsequent Gets read from storage.
func finalize(ctx context.Context, execs db.ScratchExecs, exec schema.ScratchExec, st *schema.Status) (schema.ScratchExec, error) {
	if exec.State != schema.ScratchExecPending {
		return exec, nil
	}
	exec.State = schema.ScratchExecLost
	exec.Error = st
	exec.FinishedAt = time.Now().UTC()
	if err := execs.Update(ctx, exec); err != nil {
		return exec, errors.Wrap(err, "finalize update")
	}
	return exec, nil
}

// outObjectFor returns the GCS object name for an op's merged output.
func outObjectFor(obliviousID, opID string) string {
	return obliviousID + "/" + opID + "/out"
}

// outURIFor returns the gs:// URI agents see in ExecResult.OutURI.
func outURIFor(bucket, obliviousID, opID string) string {
	return "gs://" + bucket + "/" + outObjectFor(obliviousID, opID)
}

func (s *gcsSyncer) writeOut(ctx context.Context, obliviousID, opID string, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	out := s.gcs.Bucket(s.bucket).Object(outObjectFor(obliviousID, opID))
	w := out.NewWriter(ctx)
	if _, err := bytes.NewReader(buf).WriteTo(w); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
