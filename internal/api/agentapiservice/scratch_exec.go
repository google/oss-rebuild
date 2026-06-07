// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/api/scratchworkerservice"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/api/googleapi"
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

// SyncStep is one bucket in a SyncSchedule. Until is the upper bound on
// exec age (exclusive); Interval is the minimum gap between non-terminal
// composes while the exec's age is below Until.
type SyncStep struct {
	Until    time.Duration
	Interval time.Duration
}

// DefaultSyncSchedule throttles compose frequency to bound the lifetime
// component count on the GCS composite object: 5s polls for the first
// 10 minutes, 30s thereafter.
//
// Cumulative composes:
//   - 10m:   120
//   - 1h:    220
//   - 6h:    820  (target ceiling; ~200-component cushion under the 1024 cap)
//
// A terminal sync (status.Done) is never skipped, so the final tail
// always reaches GCS regardless of where in the schedule the exec finishes.
var DefaultSyncSchedule = []SyncStep{
	{Until: 10 * time.Minute, Interval: 5 * time.Second},
	// Sentinel-large Until. This step's Interval applies for the
	// remainder of any exec's lifetime.
	{Until: math.MaxInt64, Interval: 30 * time.Second},
}

// gcsSyncer appends new worker output to the op's GCS object on each
// poll. Append is implemented as Compose (see pullOutput); the schedule
// keeps main's lifetime component count under GCS's 1024 cap.
type gcsSyncer struct {
	gcs          *storage.Client
	bucket       string
	execs        db.ScratchExecs
	workerDialer WorkerDialer
	schedule     []SyncStep
}

// SyncerOption configures a gcsSyncer.
type SyncerOption func(*gcsSyncer)

// WithSyncSchedule overrides DefaultSyncSchedule. Empty/nil keeps the default.
func WithSyncSchedule(steps []SyncStep) SyncerOption {
	return func(s *gcsSyncer) {
		if len(steps) > 0 {
			s.schedule = steps
		}
	}
}

// NewGCSSyncer returns a Syncer that appends new worker output to GCS
// and finalizes Firestore on the Done transition. Tests that need clock
// control should use [testing/synctest.Run]. The syncer uses
// [time.Now] directly with no injection hook.
func NewGCSSyncer(gcs *storage.Client, bucket string, execs db.ScratchExecs, wd WorkerDialer, opts ...SyncerOption) Syncer {
	s := &gcsSyncer{
		gcs:          gcs,
		bucket:       bucket,
		execs:        execs,
		workerDialer: wd,
		schedule:     DefaultSyncSchedule,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// intervalAt returns the minimum gap between non-terminal composes given
// the exec's current age. The last step's Interval applies indefinitely
// past its Until.
func (s *gcsSyncer) intervalAt(age time.Duration) time.Duration {
	for _, step := range s.schedule {
		if age < step.Until {
			return step.Interval
		}
	}
	return s.schedule[len(s.schedule)-1].Interval
}

// shouldCompose returns true when this sync should run pullOutput.
// Terminal syncs always run. First syncs (lastModified zero) always
// run because sinceLastCompose is effectively unbounded. Otherwise,
// the schedule's interval-for-age gates the call.
func (s *gcsSyncer) shouldCompose(now, startedAt, lastModified time.Time, done bool) bool {
	if done {
		return true
	}
	age := now.Sub(startedAt)
	return now.Sub(lastModified) >= s.intervalAt(age)
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

	// Only round-trip the tail if the worker has more bytes than GCS
	// does. The status call's TotalBytes is cheap.
	out := s.gcs.Bucket(s.bucket).Object(outObjectFor(scratch.ObliviousID, exec.ID))
	var (
		currentSize  int64
		currentGen   int64
		lastModified time.Time
	)
	if attrs, err := out.Attrs(ctx); err == nil {
		currentSize = attrs.Size
		currentGen = attrs.Generation
		lastModified = attrs.Updated
	} else if !errors.Is(err, storage.ErrObjectNotExist) {
		return exec, errors.Wrap(err, "stat out")
	}

	// Compose throttling: skip the upload if we composed recently enough
	// for our current age bucket. A terminal sync (status.Done) is never
	// skipped so the final tail always lands in GCS. A first sync
	// (lastModified zero) is also never skipped because sinceLastCompose
	// is enormous.
	if status.TotalBytes > currentSize && s.shouldCompose(time.Now().UTC(), exec.StartedAt, lastModified, status.Done) {
		if err := s.pullOutput(ctx, client, baseURL, scratch.ObliviousID, exec.ID, currentSize, currentGen); err != nil {
			return exec, errors.Wrap(err, "pull output")
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

// pullOutput streams the worker's tail past currentSize into a part
// object and composes it onto main. Race-safety:
//
//  1. Part write uses DoesNotExist. Same-offset collisions return 412
//     and the loser bails cleanly.
//  2. Compose uses GenerationMatch on main (or DoesNotExist on the
//     first sync). If another sync extended main since we Stat'd, our
//     compose returns 412 and we bail. The next poll picks up.
//
// The part is deleted on success, failure, and precondition loss. Broker
// crashes between part write and compose leave orphans; a parts/
// lifecycle rule is the deploy-side backstop.
func (s *gcsSyncer) pullOutput(ctx context.Context, client httpx.BasicClient, baseURL *url.URL, obliviousID, opID string, currentSize, currentGen int64) error {
	output := api.StreamStub[scratchworkerservice.OutputRequest, scratchworkerservice.OutputFrame](client, baseURL.JoinPath("exec/op/output"))

	mainName := outObjectFor(obliviousID, opID)
	// Part name keys on start offset only: concurrent same-offset pulls
	// collide on DoesNotExist and dedup. End offset isn't stable (the
	// worker may Stat more bytes than we saw in /status).
	partName := mainName + "/parts/" + strconv.FormatInt(currentSize, 10)
	main := s.gcs.Bucket(s.bucket).Object(mainName)
	part := s.gcs.Bucket(s.bucket).Object(partName)

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := part.If(storage.Conditions{DoesNotExist: true}).NewWriter(wctx)

	var wrote int64
	for frame, err := range output(ctx, scratchworkerservice.OutputRequest{ID: opID, Offset: currentSize}) {
		if err != nil {
			cancel()
			_ = w.Close()
			_ = part.Delete(context.Background())
			return err
		}
		n, werr := w.Write(frame.Content)
		if werr != nil {
			cancel()
			_ = w.Close()
			_ = part.Delete(context.Background())
			return errors.Wrap(werr, "gcs write part")
		}
		wrote += int64(n)
	}
	if err := w.Close(); err != nil {
		if isPreconditionFailed(err) {
			// Another sync claimed this offset. Nothing was finalized;
			// next poll re-stats and continues from the new size.
			return nil
		}
		return errors.Wrap(err, "gcs close part")
	}
	if wrote == 0 {
		// The worker had no bytes past currentSize (its snapshot
		// disagreed with the earlier /status). Drop the empty part.
		_ = part.Delete(context.Background())
		return nil
	}

	// Compose. First sync: main doesn't exist, copy part → main via a
	// single-source compose with DoesNotExist. Subsequent syncs:
	// [main, part] → main with GenerationMatch.
	var composer *storage.Composer
	if currentGen == 0 {
		composer = main.If(storage.Conditions{DoesNotExist: true}).ComposerFrom(part)
	} else {
		composer = main.If(storage.Conditions{GenerationMatch: currentGen}).ComposerFrom(main, part)
	}
	if _, err := composer.Run(ctx); err != nil {
		_ = part.Delete(context.Background())
		if isPreconditionFailed(err) {
			// Another sync extended main first. Bail cleanly; next poll
			// re-stats and continues from the new size.
			return nil
		}
		return errors.Wrap(err, "gcs compose")
	}
	_ = part.Delete(context.Background())
	return nil
}

// isPreconditionFailed reports whether err is an HTTP 412 from GCS,
// indicating a lost race on a generation / DoesNotExist precondition.
func isPreconditionFailed(err error) bool {
	var ae *googleapi.Error
	if errors.As(err, &ae) {
		return ae.Code == http.StatusPreconditionFailed
	}
	return false
}
