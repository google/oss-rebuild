// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func reapDeps(t *testing.T, scratches db.Scratch, execs db.ScratchExecs, gce GCE) *ScratchReapDeps {
	t.Helper()
	return &ScratchReapDeps{
		Scratches:     scratches,
		Execs:         execs,
		GCE:           gce,
		IdleThreshold: 30 * time.Minute,
	}
}

func TestScratchReap_IdleReapedFreshPreserved(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	gce := NewMemoryGCE()
	now := time.Now().UTC()

	zone := "us-central1-a"
	stale := schema.Scratch{
		ID: "stale", State: schema.ScratchReady, Zone: zone,
		VMName:   "scratch-stale",
		LastUsed: now.Add(-1 * time.Hour),
	}
	fresh := schema.Scratch{
		ID: "fresh", State: schema.ScratchReady, Zone: zone,
		VMName:   "scratch-fresh",
		LastUsed: now.Add(-5 * time.Minute),
	}
	dead := schema.Scratch{
		ID: "dead", State: schema.ScratchDeleted, Zone: zone,
		LastUsed: now.Add(-2 * time.Hour),
	}
	for _, s := range []schema.Scratch{stale, fresh, dead} {
		if err := scratches.Insert(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}
	if _, err := gce.InsertInstanceFromTemplate(ctx, zone, stale.VMName, "tpl", nil); err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, db.NewMemoryScratchExecs(), gce))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 1 {
		t.Errorf("ScratchesReaped = %d; want 1 (only stale)", resp.ScratchesReaped)
	}
	got, _ := scratches.Get(ctx, "stale")
	if got.State != schema.ScratchDeleted {
		t.Errorf("stale.State = %q; want deleted", got.State)
	}
	got, _ = scratches.Get(ctx, "fresh")
	if got.State != schema.ScratchReady {
		t.Errorf("fresh.State = %q; want ready (left alone)", got.State)
	}
	if gce.InstanceExists(zone, stale.VMName) {
		t.Errorf("stale VM not torn down")
	}
}

func TestScratchReap_OpOnDeadScratchFinalizesLost(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	gce := NewMemoryGCE()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-1", State: schema.ScratchDeleted, Zone: "z",
		VMName:   "vm",
		LastUsed: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-1", ScratchID: "s-1", State: schema.ScratchExecPending,
		StartedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, gce))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 0 || resp.OpsFinalized != 1 {
		t.Errorf("response = %+v; want ScratchesReaped=0 OpsFinalized=1", resp)
	}
	stored, _ := execs.Get(ctx, "op-1")
	if stored.State != schema.ScratchExecLost {
		t.Errorf("State = %q; want lost", stored.State)
	}
	if stored.FinishedAt.IsZero() {
		t.Errorf("FinishedAt not set on finalize")
	}
}

// An op without a time bound never expires and exempts its scratch from
// teardown indefinitely (the reaper warns instead): there is no deadline
// to expire against, so killing the scratch could kill a live exec.
func TestScratchReap_UnboundedOpExemptsScratch(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-h", State: schema.ScratchReady, Zone: "z",
		LastUsed: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-old", ScratchID: "s-h", State: schema.ScratchExecPending,
		StartedAt: now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 0 || resp.OpsFinalized != 0 {
		t.Errorf("response = %+v; want ScratchesReaped=0 OpsFinalized=0", resp)
	}
	if stored, _ := execs.Get(ctx, "op-old"); stored.State != schema.ScratchExecPending {
		t.Errorf("State = %q; want pending", stored.State)
	}
	if got, _ := scratches.Get(ctx, "s-h"); got.State != schema.ScratchReady {
		t.Errorf("scratch State = %q; want ready (exempt while unbounded op pending)", got.State)
	}
}

func TestScratchReap_PendingOpOnHealthyScratchStaysPending(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-h", State: schema.ScratchReady, LastUsed: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-fresh", ScratchID: "s-h", State: schema.ScratchExecPending,
		StartedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.OpsFinalized != 0 {
		t.Errorf("OpsFinalized = %d; want 0", resp.OpsFinalized)
	}
	stored, _ := execs.Get(ctx, "op-fresh")
	if stored.State != schema.ScratchExecPending {
		t.Errorf("State = %q; want pending", stored.State)
	}
}

func TestScratchReap_OrphanedOpScratchMissingEntirely(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-orphan", ScratchID: "vanished", State: schema.ScratchExecPending,
		StartedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.OpsFinalized != 1 {
		t.Errorf("OpsFinalized = %d; want 1", resp.OpsFinalized)
	}
	stored, _ := execs.Get(ctx, "op-orphan")
	if stored.State != schema.ScratchExecLost {
		t.Errorf("State = %q; want lost", stored.State)
	}
}

func TestScratchReap_EmptyState(t *testing.T) {
	resp, err := ScratchReap(context.Background(), ScratchReapRequest{}, reapDeps(t,
		db.NewMemoryScratch(), db.NewMemoryScratchExecs(), NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 0 || resp.OpsFinalized != 0 {
		t.Errorf("response = %+v; want zero/zero", resp)
	}
}

// An expired pending op no longer exempts its idle scratch: the syncer is
// invoked pre-teardown to capture output while the worker is reachable, the
// scratch is torn down, and the sweep finalizes the op TimedOut (not Lost:
// the deadline check precedes the scratch-state check).
func TestScratchReap_PreTeardownSyncerInvokedPerPendingOp(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, Zone: "z",
		LastUsed: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-1", ScratchID: "s-1", State: schema.ScratchExecPending,
		TimeoutSeconds: 3600,
		StartedAt:      now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	sync := &fakeSyncer{}
	deps := reapDeps(t, scratches, execs, NewMemoryGCE())
	deps.Syncer = sync

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if sync.calls != 1 {
		t.Errorf("Syncer.Sync calls = %d; want 1 (one pending op on reaped scratch)", sync.calls)
	}
	if resp.ScratchesReaped != 1 || resp.OpsFinalized != 1 {
		t.Errorf("response = %+v; want ScratchesReaped=1 OpsFinalized=1", resp)
	}
	stored, _ := execs.Get(ctx, "op-1")
	if stored.State != schema.ScratchExecTimedOut {
		t.Errorf("State = %q; want timed_out", stored.State)
	}
}

// A pending op inside its deadline exempts its idle scratch from teardown;
// an idle scratch without one is still reaped in the same pass.
func TestScratchReap_IdleButBusyScratchSkipped(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	for _, s := range []schema.Scratch{
		{ID: "busy-s", State: schema.ScratchReady, Zone: "z", LastUsed: now.Add(-time.Hour)},
		{ID: "idle-s", State: schema.ScratchReady, Zone: "z", LastUsed: now.Add(-time.Hour)},
	} {
		if err := scratches.Insert(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-long", ScratchID: "busy-s", State: schema.ScratchExecPending,
		TimeoutSeconds: 4 * 60 * 60,
		StartedAt:      now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 1 || resp.OpsFinalized != 0 {
		t.Errorf("response = %+v; want ScratchesReaped=1 OpsFinalized=0", resp)
	}
	if got, _ := scratches.Get(ctx, "busy-s"); got.State != schema.ScratchReady {
		t.Errorf("busy-s.State = %q; want ready (exempt while op in deadline)", got.State)
	}
	if got, _ := scratches.Get(ctx, "idle-s"); got.State != schema.ScratchDeleted {
		t.Errorf("idle-s.State = %q; want deleted", got.State)
	}
	if stored, _ := execs.Get(ctx, "op-long"); stored.State != schema.ScratchExecPending {
		t.Errorf("op-long.State = %q; want pending", stored.State)
	}
}

// The stamped per-exec timeout, not the global OpDeadline, bounds the op:
// a 60s exec is expired 20m after dispatch even though the fallback
// deadline is 2h.
func TestScratchReap_StampedTimeoutExpiryFinalizesTimedOut(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-h", State: schema.ScratchReady, LastUsed: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-short", ScratchID: "s-h", State: schema.ScratchExecPending,
		TimeoutSeconds: 60,
		StartedAt:      now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, reapDeps(t, scratches, execs, NewMemoryGCE()))
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 0 || resp.OpsFinalized != 1 {
		t.Errorf("response = %+v; want ScratchesReaped=0 OpsFinalized=1", resp)
	}
	if stored, _ := execs.Get(ctx, "op-short"); stored.State != schema.ScratchExecTimedOut {
		t.Errorf("State = %q; want timed_out", stored.State)
	}
}

// persistingSyncer finalizes the exec via Execs.Update, mimicking the
// production gcsSyncer observing the worker's Done transition.
type persistingSyncer struct {
	execs db.ScratchExecs
	calls int
}

func (p *persistingSyncer) Sync(ctx context.Context, exec schema.ScratchExec, _ schema.Scratch) (schema.ScratchExec, error) {
	p.calls++
	exec.State = schema.ScratchExecCompleted
	exec.FinishedAt = time.Now().UTC()
	if err := p.execs.Update(ctx, exec); err != nil {
		return exec, err
	}
	return exec, nil
}

// An expired op on a reachable scratch is pulled through the worker: the
// syncer's Completed result stands and the sweep does not overwrite it
// with a blind TimedOut.
func TestScratchReap_PullThroughSyncFinalizes(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-live", State: schema.ScratchReady, LastUsed: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-exp", ScratchID: "s-live", State: schema.ScratchExecPending,
		TimeoutSeconds: 60,
		StartedAt:      now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	sync := &persistingSyncer{execs: execs}
	deps := reapDeps(t, scratches, execs, NewMemoryGCE())
	deps.Syncer = sync

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if sync.calls != 1 {
		t.Errorf("Syncer.Sync calls = %d; want 1", sync.calls)
	}
	if resp.OpsFinalized != 0 {
		t.Errorf("OpsFinalized = %d; want 0 (syncer persisted the terminal state)", resp.OpsFinalized)
	}
	if stored, _ := execs.Get(ctx, "op-exp"); stored.State != schema.ScratchExecCompleted {
		t.Errorf("State = %q; want completed (pull-through result preserved)", stored.State)
	}
}

// When the pull-through sync fails, the expired op still finalizes blind.
func TestScratchReap_PullThroughSyncErrorFallsBackTimedOut(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-live", State: schema.ScratchReady, LastUsed: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-exp", ScratchID: "s-live", State: schema.ScratchExecPending,
		TimeoutSeconds: 60,
		StartedAt:      now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	deps := reapDeps(t, scratches, execs, NewMemoryGCE())
	deps.Syncer = &fakeSyncer{err: errors.New("worker unreachable")}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.OpsFinalized != 1 {
		t.Errorf("OpsFinalized = %d; want 1", resp.OpsFinalized)
	}
	if stored, _ := execs.Get(ctx, "op-exp"); stored.State != schema.ScratchExecTimedOut {
		t.Errorf("State = %q; want timed_out", stored.State)
	}
}

// The sweep re-lists pending ops rather than reusing the pre-teardown
// snapshot: an op the pre-teardown sync finalized as Completed must not be
// clobbered back to TimedOut by a stale full-record Update.
func TestScratchReap_SweepDoesNotClobberPreTeardownSync(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, Zone: "z",
		LastUsed: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-1", ScratchID: "s-1", State: schema.ScratchExecPending,
		TimeoutSeconds: 3600,
		StartedAt:      now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	sync := &persistingSyncer{execs: execs}
	deps := reapDeps(t, scratches, execs, NewMemoryGCE())
	deps.Syncer = sync

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 1 || resp.OpsFinalized != 0 {
		t.Errorf("response = %+v; want ScratchesReaped=1 OpsFinalized=0", resp)
	}
	if stored, _ := execs.Get(ctx, "op-1"); stored.State != schema.ScratchExecCompleted {
		t.Errorf("State = %q; want completed (pre-teardown sync result preserved)", stored.State)
	}
}

// bumpingSyncer stands in for an agent interaction racing the reaper:
// it bumps LastUsed during the pre-teardown sync, so the teardown
// re-check must spare the scratch.
type bumpingSyncer struct {
	scratches db.Scratch
}

func (b *bumpingSyncer) Sync(ctx context.Context, exec schema.ScratchExec, scratch schema.Scratch) (schema.ScratchExec, error) {
	if err := b.scratches.UpdateLastUsed(ctx, scratch.ID, time.Now().UTC()); err != nil {
		return exec, err
	}
	return exec, nil
}

func TestScratchReap_TeardownRecheckSparesRevivedScratch(t *testing.T) {
	ctx := context.Background()
	scratches := db.NewMemoryScratch()
	execs := db.NewMemoryScratchExecs()
	now := time.Now().UTC()

	if err := scratches.Insert(ctx, schema.Scratch{
		ID: "s-1", State: schema.ScratchReady, Zone: "z",
		LastUsed: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID: "op-1", ScratchID: "s-1", State: schema.ScratchExecPending,
		TimeoutSeconds: 3600,
		StartedAt:      now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatalf("seed exec: %v", err)
	}

	deps := reapDeps(t, scratches, execs, NewMemoryGCE())
	deps.Syncer = &bumpingSyncer{scratches: scratches}

	resp, err := ScratchReap(ctx, ScratchReapRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchReap: %v", err)
	}
	if resp.ScratchesReaped != 0 {
		t.Errorf("ScratchesReaped = %d; want 0 (LastUsed moved before teardown)", resp.ScratchesReaped)
	}
	if got, _ := scratches.Get(ctx, "s-1"); got.State != schema.ScratchReady {
		t.Errorf("State = %q; want ready (spared by re-check)", got.State)
	}
}
