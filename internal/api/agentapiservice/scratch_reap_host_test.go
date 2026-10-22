// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// TestOrphanedGuest is the guest sweep's decision table.
func TestOrphanedGuest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rec   schema.Scratch
		found bool
		want  bool
	}{
		{"NoRecordAtAll", schema.Scratch{}, false, true},
		{"RecordDeleted", schema.Scratch{Host: "h1", State: schema.ScratchDeleted}, true, true},
		{"RecordDeleting", schema.Scratch{Host: "h1", State: schema.ScratchDeleting}, true, true},
		{"RecordNamesThisHost", schema.Scratch{Host: "h1", State: schema.ScratchReady}, true, false},
		{"RecordStillStartingHere", schema.Scratch{Host: "h1", State: schema.ScratchStarting}, true, false},
		// The session was re-created elsewhere. This guest is a leftover.
		{"RecordNamesAnotherHost", schema.Scratch{Host: "h2", State: schema.ScratchReady}, true, true},
		{"RecordNamesNoHost", schema.Scratch{State: schema.ScratchStarting}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orphanedGuest(tc.rec, tc.found, "h1"); got != tc.want {
				t.Errorf("orphanedGuest = %v; want %v", got, tc.want)
			}
		})
	}
}

func reapHostDeps(gce GCE, hosts *fakeHosts, scratches db.Scratch) *ScratchReapDeps {
	return &ScratchReapDeps{
		Scratches:         scratches,
		Execs:             db.NewMemoryScratchExecs(),
		GCE:               gce,
		Hosts:             hosts.client(),
		Zones:             []string{"us-central1-a"},
		IdleThreshold:     30 * time.Minute,
		HostHealthTimeout: 50 * time.Millisecond,
		HostHealthDelay:   10 * time.Millisecond,
	}
}

func TestSweepHosts_DestroysOrphanGuests(t *testing.T) {
	now := time.Now().UTC()
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	scratches := db.NewMemoryScratch()
	gce.SeedRunning("us-central1-a", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", now)
	hosts.AddHost("h1", 6)
	hosts.SeedGuest("h1", "live", now.Add(-time.Minute))
	hosts.SeedGuest("h1", "vanished", now.Add(-time.Hour))
	hosts.SeedGuest("h1", "elsewhere", now.Add(-time.Hour))
	for _, rec := range []schema.Scratch{
		{ID: "live", Host: "h1", State: schema.ScratchReady},
		{ID: "elsewhere", Host: "h2", State: schema.ScratchReady},
	} {
		if err := scratches.Insert(context.Background(), rec); err != nil {
			t.Fatalf("seed scratch: %v", err)
		}
	}
	swept, reaped := sweepHosts(context.Background(), reapHostDeps(gce, hosts, scratches), now)
	if swept != 2 {
		t.Errorf("guests swept = %d; want 2", swept)
	}
	if reaped != 0 {
		t.Errorf("hosts reaped = %d; want 0 while a live session remains", reaped)
	}
	names := hosts.GuestNames("h1")
	if len(names) != 1 || names[0] != "live" {
		t.Errorf("guests = %v; want only the live session", names)
	}
	if !gce.InstanceExists("us-central1-a", "h1") {
		t.Error("host deleted while still serving a session")
	}
}

func TestSweepHosts_LeavesGuestsWhenTheStoreIsUnreadable(t *testing.T) {
	// A store error is not evidence of garbage.
	now := time.Now().UTC()
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	gce.SeedRunning("us-central1-a", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", now)
	hosts.AddHost("h1", 6)
	hosts.SeedGuest("h1", "s1", now.Add(-time.Hour))
	deps := reapHostDeps(gce, hosts, faultingScratches{err: errors.New("firestore boom")})
	swept, reaped := sweepHosts(context.Background(), deps, now)
	if swept != 0 || reaped != 0 {
		t.Errorf("swept, reaped = %d, %d; want nothing touched", swept, reaped)
	}
	if len(hosts.GuestNames("h1")) != 1 {
		t.Errorf("guests = %v; want the guest left alone", hosts.GuestNames("h1"))
	}
}

func TestSweepHosts_ReapsAnEmptyIdleHost(t *testing.T) {
	now := time.Now().UTC()
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	gce.SeedRunning("us-central1-a", "h-idle", hostLabels(schema.MachineClassStandard), "10.0.0.1", now.Add(-2*time.Hour))
	hosts.AddHost("h-idle", 6)
	// Empty but younger than the threshold: it stays.
	gce.SeedRunning("us-central1-a", "h-young", hostLabels(schema.MachineClassStandard), "10.0.0.2", now.Add(-time.Minute))
	hosts.AddHost("h-young", 6)
	hosts.SeedGuest("h-young", "recent", now.Add(-time.Minute))
	if err := hosts.client().Delete(context.Background(), Instance{Name: "h-young"}, "recent"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	swept, reaped := sweepHosts(context.Background(), reapHostDeps(gce, hosts, db.NewMemoryScratch()), now)
	if swept != 0 {
		t.Errorf("guests swept = %d; want 0", swept)
	}
	if reaped != 1 {
		t.Errorf("hosts reaped = %d; want only the idle one", reaped)
	}
	if gce.InstanceExists("us-central1-a", "h-idle") {
		t.Error("idle host survived; want it collected")
	}
	if !gce.InstanceExists("us-central1-a", "h-young") {
		t.Error("young host collected; want it kept for the next session")
	}
}

func TestSweepHosts_EmptiedByItsOwnSweepIsReapedInTheSamePass(t *testing.T) {
	now := time.Now().UTC()
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	gce.SeedRunning("us-central1-a", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", now.Add(-2*time.Hour))
	hosts.AddHost("h1", 6)
	hosts.SeedGuest("h1", "orphan", now.Add(-2*time.Hour))
	swept, reaped := sweepHosts(context.Background(), reapHostDeps(gce, hosts, db.NewMemoryScratch()), now)
	if swept != 1 || reaped != 1 {
		t.Errorf("swept, reaped = %d, %d; want the host collected once its last orphan went", swept, reaped)
	}
}

func TestSweepHosts_RetiresAnUnreachableHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now().UTC()
		gce := NewMemoryGCE()
		hosts := newFakeHosts()
		gce.SeedRunning("us-central1-a", "h-down", hostLabels(schema.MachineClassStandard), "10.0.0.1", now)
		hosts.AddHost("h-down", 6)
		hosts.SeedGuest("h-down", "stranded", now.Add(-time.Minute))
		// A down host answers nothing, guests included.
		hosts.FailHealthz("h-down", errors.New("connection refused"))
		hosts.FailGuests("h-down", errors.New("connection refused"))
		deps := reapHostDeps(gce, hosts, db.NewMemoryScratch())
		_, reaped := sweepHosts(context.Background(), deps, now)
		if reaped != 0 {
			t.Errorf("hosts reaped = %d; want the unreachable host retired, not deleted", reaped)
		}
		// Retired but kept: a broker behind a blip may still serve its session.
		inst, err := gce.GetInstance(context.Background(), "us-central1-a", "h-down")
		if err != nil {
			t.Fatalf("unreachable host deleted on first sight; want it retired and kept")
		}
		if _, ok := stampAt(inst, hostLabelRetiredKey); !ok {
			t.Errorf("labels = %v; want the retired stamp", inst.Labels)
		}
		// Inside the threshold it stays. Past it, it goes.
		if _, reaped := sweepHosts(context.Background(), deps, now.Add(deps.idleThreshold()/2)); reaped != 0 {
			t.Errorf("hosts reaped = %d inside the threshold; want 0", reaped)
		}
		if _, reaped := sweepHosts(context.Background(), deps, now.Add(2*deps.idleThreshold())); reaped != 1 {
			t.Errorf("hosts reaped = %d past the threshold; want the host deleted", reaped)
		}
		if gce.InstanceExists("us-central1-a", "h-down") {
			t.Error("host unreachable for the whole threshold survived; want it deleted")
		}
	})
}

func TestSweepHosts_WaitsOutEachOutageOnItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now().UTC()
		gce := NewMemoryGCE()
		hosts := newFakeHosts()
		gce.SeedRunning("us-central1-a", "h-flaky", hostLabels(schema.MachineClassStandard), "10.0.0.1", now)
		hosts.AddHost("h-flaky", 6)
		deps := reapHostDeps(gce, hosts, db.NewMemoryScratch())
		down := func(err error) { hosts.FailHealthz("h-flaky", err); hosts.FailGuests("h-flaky", err) }
		threshold := deps.idleThreshold()
		// A blip retires the host, and the next pass finds it answering.
		down(errors.New("connection refused"))
		sweepHosts(context.Background(), deps, now)
		down(nil)
		sweepHosts(context.Background(), deps, now.Add(threshold/2))
		// A second blip, long after the retirement, starts its own wait.
		down(errors.New("connection refused"))
		if _, reaped := sweepHosts(context.Background(), deps, now.Add(2*threshold)); reaped != 0 {
			t.Errorf("hosts reaped = %d on a new outage; want the host kept until the outage outlasts the threshold", reaped)
		}
		if _, reaped := sweepHosts(context.Background(), deps, now.Add(4*threshold)); reaped != 1 {
			t.Errorf("hosts reaped = %d past the threshold; want the host deleted", reaped)
		}
	})
}

func TestSweepHosts_LeavesAHostThatTookASessionDuringRetirement(t *testing.T) {
	now := time.Now().UTC()
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	gce.SeedRunning("us-central1-a", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", now.Add(-2*time.Hour))
	hosts.AddHost("h1", 6)
	// The fake admits a session between the sweep's two Guests calls, which
	// the re-check after retirement must catch.
	deps := reapHostDeps(gce, hosts, db.NewMemoryScratch())
	var calls int
	// Call 1 is the sweep's listing, call 2 deleteHost's re-check.
	hosts.onGuests = func() {
		calls++
		if calls == 2 {
			hosts.SeedGuest("h1", "late-arrival", now)
		}
	}
	_, reaped := sweepHosts(context.Background(), deps, now)
	if reaped != 0 {
		t.Errorf("hosts reaped = %d; want the host left to drain", reaped)
	}
	if !gce.InstanceExists("us-central1-a", "h1") {
		t.Error("host deleted after taking a session; want it left to drain")
	}
}

func TestSweepHosts_DisabledWithoutHosts(t *testing.T) {
	gce := NewMemoryGCE()
	gce.SeedRunning("us-central1-a", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", time.Now().UTC())
	deps := reapHostDeps(gce, nil, db.NewMemoryScratch())
	if swept, reaped := sweepHosts(context.Background(), deps, time.Now().UTC()); swept != 0 || reaped != 0 {
		t.Errorf("swept, reaped = %d, %d; want the host half of the pass disabled", swept, reaped)
	}
	if len(gce.Log()) != 0 {
		t.Errorf("gce log = %v; want no calls at all", gce.Log())
	}
}

// faultingScratches fails every read, standing in for a store outage.
type faultingScratches struct {
	db.Scratch
	err error
}

func (f faultingScratches) Get(context.Context, string) (schema.Scratch, error) {
	return schema.Scratch{}, f.err
}
