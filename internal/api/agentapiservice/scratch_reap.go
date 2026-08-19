// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	pkgerrors "github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type ScratchReapRequest struct{}

func (ScratchReapRequest) Validate() error { return nil }

// ScratchReapResponse reports the counts from a single reap cycle.
type ScratchReapResponse struct {
	ScratchesReaped int `json:"scratches_reaped"`
	OpsFinalized    int `json:"ops_finalized"`
}

// opDeadlineGrace pads each op's worker-enforced timeout to allow for
// dispatch latency, clock skew, and sync slack before the reaper treats
// the op as expired.
const opDeadlineGrace = 10 * time.Minute

// ScratchReapDeps wires the reaper.
type ScratchReapDeps struct {
	Scratches db.Scratch
	Execs     db.ScratchExecs
	GCE       GCE
	// Syncer (optional) pulls a pending op's final status from its worker
	// before the scratch is torn down or the op declared expired. nil
	// finalizes such ops blind.
	Syncer Syncer
	Zones  []string // zones a VM may sit in when its record has none (see deleteScratch)
	// IdleThreshold is how long a scratch may go without a write before
	// the reaper takes it (see db.ScratchIdleSince).
	IdleThreshold time.Duration // default: 30m
}

func (d *ScratchReapDeps) idleThreshold() time.Duration {
	if d.IdleThreshold > 0 {
		return d.IdleThreshold
	}
	return 30 * time.Minute
}

// deadlineFor returns the op's hard deadline: its worker-enforced timeout
// plus grace, anchored at broker-side creation since the worker's observed
// start only lands on the record at terminal sync. Only meaningful for
// bounded ops (TimeoutSeconds set). Ops without a bound never expire and
// exempt their scratch from teardown.
func deadlineFor(exec schema.ScratchExec) time.Time {
	return exec.CreatedAt.Add(time.Duration(exec.TimeoutSeconds)*time.Second + opDeadlineGrace)
}

// ScratchReap moves records that stopped making progress to a terminal
// state. Each sweep is best-effort per item: a failure is logged and the
// pass goes on.
//
//	sweep          candidate                                    outcome
//	idle scratch   ready, LastUsed past IdleThreshold           VM and record deleted
//	stuck scratch  starting or deleting, no write for as long   VM and record deleted
//	pending exec   past its deadline, or its scratch gone       TimedOut or Lost
//
// A pending exec inside its deadline keeps its scratch up, so a longer
// execution needs a longer exec timeout rather than a longer idle
// threshold. Idle and stuck scratches come from one listing.
func ScratchReap(ctx context.Context, _ ScratchReapRequest, deps *ScratchReapDeps) (*ScratchReapResponse, error) {
	now := time.Now().UTC()
	idleCutoff := now.Add(-deps.idleThreshold())
	// Snapshot pending ops to derive busy scratches. An unknown busy set
	// must abort the pass: reaping blind could kill active execs.
	pending, err := deps.Execs.ListPending(ctx)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, pkgerrors.Wrap(err, "list pending execs"))
	}
	busy := make(map[string]bool)
	for _, exec := range pending {
		if exec.CreatedAt.IsZero() {
			continue
		}
		if exec.TimeoutSeconds <= 0 {
			// No bound to expire against: leave the scratch up rather than
			// risk killing a live exec, but make the zombie visible.
			log.Printf("reap: op %s on scratch %s has no time bound; exempting from teardown", exec.ID, exec.ScratchID)
			busy[exec.ScratchID] = true
		} else if now.Before(deadlineFor(exec)) {
			busy[exec.ScratchID] = true
		}
	}
	// Pending ops on a scratch are synced before its teardown so exit
	// codes are captured while the worker is still reachable.
	idle, err := deps.Scratches.ListIdleSince(ctx, idleCutoff)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, pkgerrors.Wrap(err, "list idle scratches"))
	}
	var scratchesReaped int
	for _, scratch := range idle {
		if busy[scratch.ID] {
			continue
		}
		if deps.Syncer != nil {
			syncPendingFor(ctx, deps, scratch, pending)
		}
		// Re-read before the destructive step: an exec dispatched since
		// the listing bumped LastUsed, and a create may have moved on.
		cur, err := deps.Scratches.Get(ctx, scratch.ID)
		if err != nil {
			log.Printf("reap re-check scratch %s: %v", scratch.ID, err)
			continue
		}
		if !db.ScratchIdleSince(cur, idleCutoff) {
			continue
		}
		if err := deleteScratch(ctx, deps.Scratches, deps.GCE, cur, deps.Zones); err != nil {
			log.Printf("reap teardown scratch %s: %v", scratch.ID, err)
			continue
		}
		scratchesReaped++
	}
	// Sweep pending ops. Re-list rather than reuse the snapshot: the
	// pre-teardown sync may have finalized some, and Execs.Update is a
	// full-record overwrite that would put them back to Pending.
	pending, err = deps.Execs.ListPending(ctx)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, pkgerrors.Wrap(err, "list pending execs"))
	}
	var opsFinalized int
	for _, exec := range pending {
		next, errStatus := terminalStateFor(ctx, deps, exec, now)
		if next == schema.ScratchExecPending {
			continue
		}
		exec.State = next
		exec.Error = errStatus
		exec.FinishedAt = now
		exec.Updated = now
		if err := deps.Execs.Update(ctx, exec); err != nil {
			log.Printf("reap finalize op %s: %v", exec.ID, err)
			continue
		}
		opsFinalized++
	}
	return &ScratchReapResponse{ScratchesReaped: scratchesReaped, OpsFinalized: opsFinalized}, nil
}

// syncPendingFor invokes Syncer for each pending op on scratch. Each op
// gets a final update before the teardown loses access.
func syncPendingFor(ctx context.Context, deps *ScratchReapDeps, scratch schema.Scratch, pending []schema.ScratchExec) {
	for _, exec := range pending {
		if exec.ScratchID != scratch.ID {
			continue
		}
		if _, err := deps.Syncer.Sync(ctx, exec, scratch); err != nil {
			log.Printf("reap pre-teardown sync op %s: %v", exec.ID, err)
		}
	}
}

// terminalStateFor returns the State to transition exec to in this reap
// pass plus the diagnostic Status to attach, or ScratchExecPending with a
// nil Status if it should stay pending or was already finalized here (an
// expired op pulled through a still-reachable worker is persisted by the
// Syncer itself).
//
// The deadline check stays ahead of the scratch-state checks so ops on a
// scratch torn down earlier in the pass finalize TimedOut, not Lost.
func terminalStateFor(ctx context.Context, deps *ScratchReapDeps, exec schema.ScratchExec, now time.Time) (schema.ScratchExecState, *schema.Status) {
	if exec.TimeoutSeconds > 0 && !exec.CreatedAt.IsZero() && now.After(deadlineFor(exec)) {
		// The worker killed the command at its timeout, so a reachable
		// worker has the real exit status and output. Pull those through
		// before falling back to a blind TimedOut.
		if deps.Syncer != nil {
			if scratch, err := deps.Scratches.Get(ctx, exec.ScratchID); err == nil && scratch.State == schema.ScratchReady {
				if synced, err := deps.Syncer.Sync(ctx, exec, scratch); err != nil {
					log.Printf("reap pull-through sync op %s: %v", exec.ID, err)
				} else if synced.State != schema.ScratchExecPending {
					return schema.ScratchExecPending, nil
				}
			}
		}
		return schema.ScratchExecTimedOut, &schema.Status{
			Code:    int(codes.DeadlineExceeded),
			Message: "reaper: op past hard deadline",
		}
	}
	if exec.ScratchID == "" {
		return schema.ScratchExecPending, nil
	}
	scratch, err := deps.Scratches.Get(ctx, exec.ScratchID)
	if errors.Is(err, db.ErrNotFound) {
		return schema.ScratchExecLost, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: "reaper: scratch not found",
		}
	}
	if err != nil {
		// Don't fail the op on a transient store error; the next reap pass retries.
		log.Printf("reap scratches.Get(%s): %v", exec.ScratchID, err)
		return schema.ScratchExecPending, nil
	}
	if scratch.State != schema.ScratchReady && scratch.State != schema.ScratchStarting {
		return schema.ScratchExecLost, &schema.Status{
			Code:    int(codes.Unavailable),
			Message: fmt.Sprintf("reaper: scratch %s not ready (state=%s)", scratch.ID, scratch.State),
		}
	}
	return schema.ScratchExecPending, nil
}
