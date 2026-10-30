// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

func topUpDeps(gce *MemoryGCE, hosts *fakeHosts, scratches db.Scratch, headroom int) *ScratchCreateDeps {
	deps := newCreateDeps(gce, hosts, scratches, nil)
	deps.Standard.Headroom = headroom
	deps.Standard.Slots = 8
	return deps
}

func inserts(gce *MemoryGCE) int {
	n := 0
	for _, call := range gce.Log() {
		if strings.HasPrefix(call, "InsertInstanceFromTemplate") {
			n++
		}
	}
	return n
}

func TestScratchTopUp_MintsUpToHeadroomOnce(t *testing.T) {
	gce, hosts := NewMemoryGCE(), newFakeHosts()
	// Minted hosts are still starting, so they report no guests yet.
	hosts.AutoProvision(0)
	deps := topUpDeps(gce, hosts, db.NewMemoryScratch(), 24)
	got, err := ScratchTopUp(context.Background(), ScratchTopUpRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchTopUp: %v", err)
	}
	// Eight slots per host.
	if got.HostsMinted != 3 || inserts(gce) != 3 {
		t.Errorf("HostsMinted = %d, inserts = %d; want 3 hosts for 24 slots", got.HostsMinted, inserts(gce))
	}
	// The booting hosts' slots count, so the next pass mints none.
	got, err = ScratchTopUp(context.Background(), ScratchTopUpRequest{}, deps)
	if err != nil {
		t.Fatalf("ScratchTopUp again: %v", err)
	}
	if got.HostsMinted != 0 {
		t.Errorf("second pass HostsMinted = %d; want 0 while the first pass's hosts boot", got.HostsMinted)
	}
}

func TestScratchTopUp_CountsReadyAndBootingSlots(t *testing.T) {
	gce, hosts := NewMemoryGCE(), newFakeHosts()
	// A ready host with 6 of 8 slots free.
	seedHost(gce, hosts, "us-central1-a", "h-ready", "10.0.0.1", 8)
	hosts.SeedGuest("h-ready", "g1", time.Now())
	hosts.SeedGuest("h-ready", "g2", time.Now())
	// A host still booting, whose 8 slots count already.
	gce.SeedRunning("us-central1-a", "h-boot", hostLabels(schema.MachineClassStandard, testStandard().InstanceTemplate), "10.0.0.2", time.Now().UTC())
	hosts.FailGuests("h-boot", errors.New("connection refused"))
	// One still unable to report its guests past the boot window counts for nothing.
	gce.SeedRunning("us-central1-a", "h-dead", hostLabels(schema.MachineClassStandard, testStandard().InstanceTemplate), "10.0.0.3", time.Now().UTC().Add(-5*time.Minute))
	hosts.FailGuests("h-dead", errors.New("connection refused"))
	got, err := ScratchTopUp(context.Background(), ScratchTopUpRequest{}, topUpDeps(gce, hosts, db.NewMemoryScratch(), 16))
	if err != nil {
		t.Fatalf("ScratchTopUp: %v", err)
	}
	// 6 + 8 = 14 free, so one more host reaches 16.
	if got.HostsMinted != 1 {
		t.Errorf("HostsMinted = %d; want 1", got.HostsMinted)
	}
}

func TestScratchTopUp_SkipsClassesWithoutHeadroom(t *testing.T) {
	gce, hosts := NewMemoryGCE(), newFakeHosts()
	got, err := ScratchTopUp(context.Background(), ScratchTopUpRequest{}, topUpDeps(gce, hosts, db.NewMemoryScratch(), 0))
	if err != nil {
		t.Fatalf("ScratchTopUp: %v", err)
	}
	if got.HostsMinted != 0 || inserts(gce) != 0 {
		t.Errorf("HostsMinted = %d; want no hosts without headroom", got.HostsMinted)
	}
}

func TestScratchTopUp_StopsAtAStockout(t *testing.T) {
	gce, hosts := NewMemoryGCE(), newFakeHosts()
	// All three inserts the pass starts find every zone out.
	for range 3 {
		gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	}
	got, err := ScratchTopUp(context.Background(), ScratchTopUpRequest{}, topUpDeps(gce, hosts, db.NewMemoryScratch(), 24))
	if err != nil {
		t.Fatalf("ScratchTopUp: %v; want the pass to end quietly", err)
	}
	if got.HostsMinted != 0 {
		t.Errorf("HostsMinted = %d; want none once every zone is out", got.HostsMinted)
	}
}
