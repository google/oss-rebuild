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
	// Syncer (optional) is invoked for every pending op on an idle
	// scratch BEFORE teardown, and for expired ops during the sweep, so
	// we capture the worker's final status while it's still reachable.
	// nil disables; affected ops get finalized blind instead.
	Syncer Syncer
	// IdleThreshold: non-deleted scratches with no in-deadline pending
	// exec and LastUsed older than this get torn down.
	IdleThreshold time.Duration // default: 30m
}

func (d *ScratchReapDeps) idleThreshold() time.Duration {
	if d.IdleThreshold > 0 {
		return d.IdleThreshold
	}
	return 30 * time.Minute
}

// deadlineFor returns the op's hard deadline: its worker-enforced timeout
// plus grace. Only meaningful for bounded ops (TimeoutSeconds set); ops
// without a bound never expire and exempt their scratch from teardown.
func deadlineFor(exec schema.ScratchExec) time.Time {
	return exec.StartedAt.Add(time.Duration(exec.TimeoutSeconds)*time.Second + opDeadlineGrace)
}

// ScratchReap deletes stale scratches and finalizes obsolete ops.
//
// Scratch teardown:
//   - Stale covers idle Ready scratches and Starting/Deleting records
//     stranded by crashed creates or failed teardowns.
//   - A scratch is never torn down while an op may still be running on
//     it (pending, within deadline). The idle threshold only applies
//     once all ops have completed.
//   - Deletes converge across passes since records only reach Deleted
//     once the VM is confirmed gone.
//
// Op finalization:
//   - Ops on a non-Ready scratch are marked Lost.
//   - Expired ops are synced and finalized, marking them TimedOut if
//     still incomplete.
//
// Best-effort: per-item failures are logged and the pass continues.
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
		if exec.StartedAt.IsZero() {
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
	// Reap stale, non-busy scratches. Before tearing down a Ready one, try
	// to sync any pending ops on it so we capture exit codes while the
	// worker is still reachable. Starting scratches never dispatched and
	// Deleting ones already had their pre-teardown sync.
	stale, err := deps.Scratches.ListIdleSince(ctx, idleCutoff)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, pkgerrors.Wrap(err, "list stale scratches"))
	}
	var scratchesReaped int
	for _, scratch := range stale {
		if busy[scratch.ID] {
			continue
		}
		if deps.Syncer != nil && scratch.State == schema.ScratchReady {
			syncPendingFor(ctx, deps, scratch, pending)
		}
		// Re-check before the destructive step: an exec dispatched after
		// the stale snapshot bumps LastUsed, and a Starting scratch may
		// have come Ready under a still-running create.
		if cur, err := deps.Scratches.Get(ctx, scratch.ID); err != nil {
			log.Printf("reap re-check scratch %s: %v", scratch.ID, err)
			continue
		} else if cur.State != scratch.State || !cur.LastUsed.Before(idleCutoff) {
			continue
		}
		if err := deleteScratch(ctx, deps.Scratches, deps.GCE, scratch); err != nil {
			log.Printf("reap scratch %s: %v", scratch.ID, err)
			continue
		}
		scratchesReaped++
	}
	// Sweep pending ops. Mark ops Lost whose scratch is gone and finalize
	// expired ones. Re-list rather than reuse the earlier snapshot so ops
	// the pre-teardown sync already finalized are skipped up front.
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
		if _, err := deps.Execs.Finalize(ctx, exec); errors.Is(err, db.ErrUnchanged) {
			continue // a concurrent finalizer won; its record stands
		} else if err != nil {
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
	if exec.TimeoutSeconds > 0 && !exec.StartedAt.IsZero() && now.After(deadlineFor(exec)) {
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
