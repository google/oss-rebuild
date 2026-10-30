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
	"github.com/google/oss-rebuild/internal/api/scratchhostservice"
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

// ScratchExecCreateDeps wires ScratchExecCreate.
type ScratchExecCreateDeps struct {
	Scratches    db.Scratch
	Execs        db.ScratchExecs
	Dialer       HostDialer
	OutputBucket string
	IDGen        func() string // For op IDs. If nil, defaults to uuid.New()
	OpTimeout    time.Duration // Default and max exec timeout. Zero refuses all
	Syncer       Syncer        // For the optimistic wait. Disabled on nil
}

// ScratchExecGetDeps wires ScratchExecGet.
type ScratchExecGetDeps struct {
	Scratches db.Scratch
	Execs     db.ScratchExecs
	Syncer    Syncer // to sync the op output. Disabled on nil
}

// ScratchExecKillDeps wires ScratchExecKill.
type ScratchExecKillDeps struct {
	Scratches db.Scratch
	Execs     db.ScratchExecs
	Dialer    HostDialer
}

// Syncer abstracts the broker-side fetch-and-persist loop. The
// production impl pulls /exec/op/status and /exec/op/output from the
// host broker, overwrites the op's GCS object with the full buffer
// (when new bytes are available), and updates Firestore on the Done
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
			CreatedAt:  e.CreatedAt,
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
// the eventual Result, dispatches the work to the host broker, and returns the
// projected operation.
//
// When the request sets WaitSeconds and a Syncer is configured, the call holds
// open up to that long (capped at maxOptimisticWait) polling for completion, so
// fast commands return a terminal operation in a single round trip. The wait is
// best-effort: expiry returns the Pending operation as usual.
//
// API-error vs Operation-error contract: failures that happen before the exec
// record is durably inserted surface as API status errors (the operation has no
// identity yet). Failures after Insert succeeds become part of the operation's
// state: the exec is finalized as Failed and returned as a successful response
// whose Operation.Error carries the reason. This matches the longrunning idiom
// and matches what Get would return for the same op id.
func ScratchExecCreate(ctx context.Context, req schema.ScratchExecRequest, deps *ScratchExecCreateDeps) (*longrunning.Operation[schema.ScratchExecResult], error) {
	scratch, err := readyScratch(ctx, deps.Scratches, req.ScratchID)
	if err != nil {
		return nil, err
	}
	if scratch.ObliviousID == "" {
		return nil, api.AsStatus(codes.Internal, errors.Errorf("scratch %q missing ObliviousID", req.ScratchID))
	}
	// Every exec gets a host-enforced bound, defaulting to and capped at
	// OpTimeout. The reaper derives deadlines from it, so an unbounded exec is
	// refused.
	maxTimeout := int(deps.OpTimeout.Seconds())
	if maxTimeout > 0 && req.TimeoutSeconds > maxTimeout {
		return nil, api.AsStatus(codes.InvalidArgument, errors.Errorf("timeout_seconds %d exceeds maximum %d", req.TimeoutSeconds, maxTimeout))
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = maxTimeout
	}
	if timeoutSeconds <= 0 {
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("timeout_seconds is required: the broker has no default exec deadline configured"))
	}

	// Prepare the host client before Insert so a dialer failure (auth setup,
	// URL parse) is a request-time error rather than a dangling Pending record.
	client, baseURL, err := deps.Dialer(hostOf(scratch))
	if err != nil {
		return nil, api.AsStatus(codes.Unavailable, errors.Wrap(err, "host dialer"))
	}

	opID := mintID(deps.IDGen)
	exec := schema.ScratchExec{
		ID:             opID,
		ScratchID:      req.ScratchID,
		Cmd:            req.Cmd,
		Cwd:            req.Cwd,
		TimeoutSeconds: timeoutSeconds,
		State:          schema.ScratchExecPending,
		OutURI:         outURIFor(deps.OutputBucket, scratch.ObliviousID, opID),
		CreatedAt:      time.Now().UTC(),
		Updated:        time.Now().UTC(),
	}
	if err := deps.Execs.Insert(ctx, exec); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "execs insert"))
	}

	// Past this point the operation has identity. Any failure becomes terminal
	// Operation state, not an API error.

	startStub := api.Stub[scratchhostservice.StartRequest, act.NoOutput](client, baseURL.JoinPath("exec/start"))
	if _, err := startStub(ctx, scratchhostservice.StartRequest{
		OpID:           opID,
		ScratchID:      req.ScratchID,
		Cmd:            req.Cmd,
		Cwd:            req.Cwd,
		Env:            req.Env,
		StdinB64:       req.StdinB64,
		TimeoutSeconds: timeoutSeconds,
	}); err != nil {
		log.Printf("scratch %q exec %q: host dispatch: %v", req.ScratchID, opID, err)
		exec, ferr := finalize(ctx, deps.Execs, exec, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: errors.Wrap(err, "host dispatch").Error(),
		})
		if ferr != nil {
			return nil, api.AsStatus(codes.Internal, errors.Wrapf(ferr, "scratch %q exec %q", req.ScratchID, opID))
		}
		return new(ProjectScratchExec(exec)), nil
	}

	// Bump LastUsed so a future reaper doesn't pick up a freshly-dispatched
	// scratch. Best effort: a transient failure here doesn't block the op.
	if err := deps.Scratches.UpdateLastUsed(ctx, req.ScratchID, time.Now().UTC()); err != nil {
		log.Printf("scratch %q exec %q: update last_used: %v", req.ScratchID, opID, err)
	}

	if req.WaitSeconds > 0 && deps.Syncer != nil {
		exec = optimisticWait(ctx, deps.Syncer, exec, scratch, time.Duration(req.WaitSeconds)*time.Second)
	}

	return new(ProjectScratchExec(exec)), nil
}

// Bounds for the optimistic wait in ScratchExecCreate. The wait exists to
// catch trivial commands (cat, ls, quick greps) in a single round trip.
// The max is low and anything longer-lived should go through the normal poll.
const (
	optimisticWaitMax      = 30 * time.Second
	optimisticPollInterval = 500 * time.Millisecond
)

// optimisticWait polls the Syncer until the exec turns terminal or the
// wait expires, returning the latest observed exec. The wait is
// best-effort, so sync errors are logged and retried rather than
// finalizing the op as ScratchExecGet would.
func optimisticWait(ctx context.Context, syncer Syncer, exec schema.ScratchExec, scratch schema.Scratch, wait time.Duration) schema.ScratchExec {
	deadline := time.Now().Add(min(wait, optimisticWaitMax))
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return exec
		}
		select {
		case <-ctx.Done():
			return exec
		case <-time.After(min(optimisticPollInterval, remaining)):
		}
		synced, err := syncer.Sync(ctx, exec, scratch)
		if err != nil {
			log.Printf("scratch %q exec %q: optimistic sync: %v", exec.ScratchID, exec.ID, err)
		} else {
			exec = synced
			if exec.State != schema.ScratchExecPending {
				return exec
			}
		}
	}
}

// ScratchExecGet returns the current state of an exec op. If the op is
// pending and a Syncer is configured, ScratchExecGet triggers a sync:
// pulling the host broker's latest status + any new output bytes, rolling
// those bytes into the op's GCS object, and (on the Done transition)
// finalizing Firestore and bumping the scratch's LastUsed so the agent
// has a fresh idle window to act on the result.
//
// Sync errors follow the last-error-is-final policy: any failure to reach the
// host, read its state, or persist the output transitions the exec to Failed
// immediately. We deliberately do not retry transient errors. For in-VPC
// broker-to-host traffic the blip rate is low enough that an occasional false
// Failed is the right cost vs. an exec that lingers in Pending indefinitely.
func ScratchExecGet(ctx context.Context, req schema.GetOperationRequest, deps *ScratchExecGetDeps) (*longrunning.Operation[schema.ScratchExecResult], error) {
	exec, err := getExec(ctx, deps.Execs, req.ID)
	if err != nil {
		return nil, err
	}
	if exec.State != schema.ScratchExecPending || deps.Syncer == nil || exec.ScratchID == "" {
		return new(ProjectScratchExec(exec)), nil
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
		return new(ProjectScratchExec(exec)), nil
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
		return new(ProjectScratchExec(exec)), nil
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
		return new(ProjectScratchExec(exec)), nil
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
	return new(ProjectScratchExec(synced)), nil
}

// ScratchExecKill forwards a kill for a pending exec to its host broker.
// Killing a finished op succeeds, so callers racing the command's exit need
// not tell the cases apart. The next sync records the kill.
func ScratchExecKill(ctx context.Context, req schema.ScratchExecKillRequest, deps *ScratchExecKillDeps) (*act.NoOutput, error) {
	exec, err := getExec(ctx, deps.Execs, req.ID)
	if err != nil {
		return nil, err
	}
	if exec.State != schema.ScratchExecPending {
		return &act.NoOutput{}, nil
	}
	scratch, err := readyScratch(ctx, deps.Scratches, exec.ScratchID)
	if err != nil {
		return nil, err
	}
	client, baseURL, err := deps.Dialer(hostOf(scratch))
	if err != nil {
		return nil, api.AsStatus(codes.Unavailable, errors.Wrap(err, "host dialer"))
	}
	killStub := api.Stub[scratchhostservice.OpRequest, act.NoOutput](client, baseURL.JoinPath("exec/op/kill"))
	if _, err := killStub(ctx, scratchhostservice.OpRequest{ID: exec.ID}); err != nil {
		return nil, api.AsStatus(codes.Unavailable, errors.Wrap(err, "host kill"))
	}
	return &act.NoOutput{}, nil
}

// getExec loads an exec record, as an API status error when it is missing.
func getExec(ctx context.Context, execs db.ScratchExecs, id string) (schema.ScratchExec, error) {
	exec, err := execs.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return schema.ScratchExec{}, api.AsStatus(codes.NotFound, errors.Errorf("op %q not found", id))
		}
		return schema.ScratchExec{}, api.AsStatus(codes.Internal, errors.Wrap(err, "execs get"))
	}
	return exec, nil
}

// readyScratch loads a scratch record that must be Ready to serve an exec,
// as API status errors otherwise.
func readyScratch(ctx context.Context, scratches db.Scratch, id string) (schema.Scratch, error) {
	scratch, err := scratches.Get(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return schema.Scratch{}, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", id))
		}
		return schema.Scratch{}, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}
	if scratch.State != schema.ScratchReady {
		return schema.Scratch{}, api.AsStatus(codes.FailedPrecondition, errors.Errorf("scratch %q not ready (state=%s)", id, scratch.State))
	}
	return scratch, nil
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

// gcsSyncer appends new host-side output to the op's GCS object on each
// poll. Append is implemented as Compose (see pullOutput); the schedule
// keeps main's lifetime component count under GCS's 1024 cap.
type gcsSyncer struct {
	gcs      *storage.Client
	bucket   string
	execs    db.ScratchExecs
	dial     HostDialer
	schedule []SyncStep
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

// NewGCSSyncer returns a Syncer that appends new host-side output to GCS
// and finalizes Firestore on the Done transition. Tests that need clock
// control should use [testing/synctest.Run]. The syncer uses
// [time.Now] directly with no injection hook.
func NewGCSSyncer(gcs *storage.Client, bucket string, execs db.ScratchExecs, dial HostDialer, opts ...SyncerOption) Syncer {
	s := &gcsSyncer{
		gcs:      gcs,
		bucket:   bucket,
		execs:    execs,
		dial:     dial,
		schedule: DefaultSyncSchedule,
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
func (s *gcsSyncer) shouldCompose(now, createdAt, lastModified time.Time, done bool) bool {
	if done {
		return true
	}
	age := now.Sub(createdAt)
	return now.Sub(lastModified) >= s.intervalAt(age)
}

func (s *gcsSyncer) Sync(ctx context.Context, exec schema.ScratchExec, scratch schema.Scratch) (schema.ScratchExec, error) {
	client, baseURL, err := s.dial(hostOf(scratch))
	if err != nil {
		return exec, errors.Wrap(err, "host client")
	}

	statusStub := api.Stub[scratchhostservice.OpRequest, scratchhostservice.ExecStatus](client, baseURL.JoinPath("exec/op/status"))
	status, err := statusStub(ctx, scratchhostservice.OpRequest{ID: exec.ID})
	if err != nil {
		return exec, errors.Wrap(err, "host status")
	}

	// Only round-trip the tail if the host has more bytes than GCS
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

	known := currentSize // bytes out is known to hold
	// Compose throttling: skip the upload if we composed recently enough
	// for our current age bucket. A terminal sync (status.Done) is never
	// skipped so the final tail always lands in GCS. A first sync
	// (lastModified zero) is also never skipped because sinceLastCompose
	// is enormous.
	if status.TotalBytes > currentSize && s.shouldCompose(time.Now().UTC(), exec.CreatedAt, lastModified, status.Done) {
		n, err := s.pullOutput(ctx, client, baseURL, scratch.ObliviousID, exec.ID, currentSize, currentGen)
		if err != nil {
			return exec, errors.Wrap(err, "pull output")
		}
		known += n
	}

	if !status.Done {
		return exec, nil
	}
	if known < status.TotalBytes {
		// The pull lost a race and the winner's compose may not have
		// landed. A terminal record is never synced again, so stay Pending.
		log.Printf("exec %q: output holds %d of %d bytes, deferring finalization", exec.ID, known, status.TotalBytes)
		return exec, nil
	}

	final := finalFromStatus(exec, status, time.Now().UTC())
	if err := s.execs.Update(ctx, final); err != nil {
		return exec, errors.Wrap(err, "execs update final")
	}
	return final, nil
}

// finalFromStatus projects a terminal host status onto the exec record.
// Execution times are the ledger's, one clock for both ends. StartedAt
// stays zero when the ledger saw no start, and FinishedAt falls back to
// now.
func finalFromStatus(exec schema.ScratchExec, status *scratchhostservice.ExecStatus, now time.Time) schema.ScratchExec {
	final := exec
	final.ExitCode = status.ExitCode
	final.StartedAt = status.StartedAt
	final.Updated = now
	if !status.FinishedAt.IsZero() {
		final.FinishedAt = status.FinishedAt
	} else {
		final.FinishedAt = now
	}
	switch {
	case status.OutputCapped:
		// Output cap: terminal like a timeout. ResourceExhausted keeps callers
		// from reporting it as a deadline.
		final.State = schema.ScratchExecTimedOut
		final.Error = &schema.Status{
			Code:    int(codes.ResourceExhausted),
			Message: errors.Errorf("output exceeded %d bytes", status.TotalBytes).Error(),
		}
	case status.Killed:
		// A killed command has no exit of its own, so the op is Lost rather
		// than Completed. Output captured before the kill remains in OutURI.
		final.State = schema.ScratchExecLost
		final.Error = &schema.Status{
			Code:    int(codes.Aborted),
			Message: "op killed",
		}
	case status.TimedOut:
		// Host-enforced timeout: command was killed at its deadline.
		// We observed the kill exit (typically 124) and partial output
		// remains in OutURI.
		final.State = schema.ScratchExecTimedOut
		final.Error = &schema.Status{
			Code:    int(codes.DeadlineExceeded),
			Message: "command exceeded TimeoutSeconds",
		}
	case status.ErrMsg != "":
		// Host reported an infra failure (spawn failed, stdin decode, ...).
		// We do not have a known exit, so treat as Lost.
		final.State = schema.ScratchExecLost
		final.Error = &schema.Status{
			Code:    int(codes.Internal),
			Message: status.ErrMsg,
		}
	default:
		final.State = schema.ScratchExecCompleted
	}
	return final
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
	exec.Updated = exec.FinishedAt
	if err := execs.Update(ctx, exec); err != nil {
		return exec, errors.Wrap(err, "finalize update")
	}
	return exec, nil
}

// outObjectFor returns the GCS object name for an op's merged output.
func outObjectFor(obliviousID, opID string) string {
	return obliviousID + "/" + opID + "/out"
}

// partObjectFor names one pull's part object under parts/, where a lifecycle rule deletes orphans.
func partObjectFor(obliviousID, opID string, offset int64) string {
	return "parts/" + obliviousID + "/" + opID + "/" + strconv.FormatInt(offset, 10)
}

// outURIFor returns the gs:// URI agents see in ExecResult.OutURI.
func outURIFor(bucket, obliviousID, opID string) string {
	return "gs://" + bucket + "/" + outObjectFor(obliviousID, opID)
}

// pullOutput streams the host's tail past currentSize into a part
// object and composes it onto main. Race-safety:
//
//  1. Part write uses DoesNotExist. Same-offset collisions return 412
//     and the loser bails cleanly.
//  2. Compose uses GenerationMatch on main (or DoesNotExist on the
//     first sync). If another sync extended main since we Stat'd, our
//     compose returns 412 and we bail. The next poll picks up.
//
// The part is deleted on success, failure, and precondition loss. Broker
// crashes between part write and compose leave orphans, which the parts/
// lifecycle rule sweeps.
//
// It returns the number of bytes it appended, zero when it lost a race or
// had nothing to add.
func (s *gcsSyncer) pullOutput(ctx context.Context, client httpx.BasicClient, baseURL *url.URL, obliviousID, opID string, currentSize, currentGen int64) (int64, error) {
	output := api.StreamStub[scratchhostservice.OutputRequest, scratchhostservice.OutputFrame](client, baseURL.JoinPath("exec/op/output"))

	mainName := outObjectFor(obliviousID, opID)
	// Part name keys on start offset only: concurrent same-offset pulls
	// collide on DoesNotExist and dedup. End offset isn't stable (the
	// host may Stat more bytes than we saw in /status).
	partName := partObjectFor(obliviousID, opID, currentSize)
	main := s.gcs.Bucket(s.bucket).Object(mainName)
	part := s.gcs.Bucket(s.bucket).Object(partName)

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := part.If(storage.Conditions{DoesNotExist: true}).NewWriter(wctx)

	var wrote int64
	for frame, err := range output(ctx, scratchhostservice.OutputRequest{ID: opID, Offset: currentSize}) {
		if err != nil {
			cancel()
			_ = w.Close()
			_ = part.Delete(context.Background())
			return 0, err
		}
		n, werr := w.Write(frame.Content)
		if werr != nil {
			cancel()
			_ = w.Close()
			_ = part.Delete(context.Background())
			return 0, errors.Wrap(werr, "gcs write part")
		}
		wrote += int64(n)
	}
	if err := w.Close(); err != nil {
		if isPreconditionFailed(err) {
			// Another sync claimed this offset. Nothing was finalized;
			// next poll re-stats and continues from the new size.
			return 0, nil
		}
		return 0, errors.Wrap(err, "gcs close part")
	}
	if wrote == 0 {
		// The host had no bytes past currentSize (its snapshot
		// disagreed with the earlier /status). Drop the empty part.
		_ = part.Delete(context.Background())
		return 0, nil
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
			return 0, nil
		}
		return 0, errors.Wrap(err, "gcs compose")
	}
	_ = part.Delete(context.Background())
	return wrote, nil
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
