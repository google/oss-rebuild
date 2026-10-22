// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"maps"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// hostSweepConcurrency bounds the fan-out over hosts.
const hostSweepConcurrency = 8

// hostLabelUnreachableKey stamps when the reaper found a host unreachable. A
// pass that reaches the host clears it, so each outage waits out its own
// threshold before the delete.
const hostLabelUnreachableKey = "scratch_unreachable"

// listPurpose returns every running scratch host, retired ones too.
// TODO: Reap stopped hosts, which keep their disks but never list here.
func listPurpose(ctx context.Context, gce GCE, zones []string) []Instance {
	return listHosts(ctx, gce, zones, map[string]string{hostLabelPurposeKey: hostLabelPurposeVal})
}

// sweepHosts applies the host reap rules to every scratch host, best effort
// per host. See ScratchReap.
func sweepHosts(ctx context.Context, deps *ScratchReapDeps, now time.Time) (guestsSwept, hostsReaped int) {
	if deps.Hosts == nil || len(deps.Zones) == 0 {
		return 0, 0
	}
	var mu sync.Mutex
	runConcurrently(ctx, listPurpose(ctx, deps.GCE, deps.Zones), hostSweepConcurrency, func(ctx context.Context, host Instance) {
		guests, reaped := sweepHost(ctx, deps, host, now)
		mu.Lock()
		defer mu.Unlock()
		guestsSwept += guests
		if reaped {
			hostsReaped++
		}
	})
	return guestsSwept, hostsReaped
}

func sweepHost(ctx context.Context, deps *ScratchReapDeps, host Instance, now time.Time) (int, bool) {
	if !hostReachable(ctx, deps, host) {
		// Retire only: a restarting broker may still serve a session. Delete once
		// it has been unreachable for the idle threshold.
		if at, ok := stampAt(host, hostLabelUnreachableKey); ok && olderThan(at, now, deps.idleThreshold()) {
			log.Printf("reap: host %s unreachable since %s; deleting", host.Name, at.Format(time.RFC3339))
			return 0, deleteHost(ctx, deps, host, now)
		}
		log.Printf("reap: host %s unreachable after %s; retiring", host.Name, deps.hostHealthTimeout())
		if err := stamp(ctx, deps.GCE, host, now, hostLabelRetiredKey, hostLabelUnreachableKey); err != nil {
			log.Printf("reap: retiring host %s: %v", host.Name, err)
		}
		return 0, false
	}
	if _, ok := host.Labels[hostLabelUnreachableKey]; ok {
		// The host answered, so its next outage starts a new wait. It stays retired.
		labels := maps.Clone(host.Labels)
		delete(labels, hostLabelUnreachableKey)
		if err := deps.GCE.SetLabels(ctx, host.Zone, host.Name, labels, host.LabelFingerprint); err != nil {
			log.Printf("reap: clearing host %s's unreachable stamp: %v", host.Name, err)
		}
	}
	list, err := deps.Hosts.Guests(ctx, host)
	if err != nil {
		log.Printf("reap: listing guests on %s: %v", host.Name, err)
		return 0, false
	}
	var swept int
	for _, g := range list.Guests {
		rec, err := deps.Scratches.Get(ctx, g.ScratchID)
		found := err == nil
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			// A store error is not evidence of garbage. Leave the guest.
			log.Printf("reap: scratch %s for guest on %s: %v", g.ScratchID, host.Name, err)
			continue
		}
		if !orphanedGuest(rec, found, host.Name) {
			continue
		}
		log.Printf("reap: destroying orphan guest %s on %s", g.ScratchID, host.Name)
		if err := deps.Hosts.Delete(ctx, host, g.ScratchID); err != nil {
			log.Printf("reap: deleting guest %s on %s: %v", g.ScratchID, host.Name, err)
			continue
		}
		swept++
	}
	if swept < len(list.Guests) {
		return swept, false
	}
	// Empty past the idle threshold. LastServed falls back to the host's
	// creation, so a broker that never reported one cannot look abandoned.
	lastServed := list.LastServed
	if lastServed.IsZero() {
		lastServed = host.Created
	}
	if !olderThan(lastServed, now, deps.idleThreshold()) {
		return swept, false
	}
	return swept, deleteHost(ctx, deps, host, now)
}

// orphanedGuest reports whether a guest is garbage: its record is missing,
// names another host, or is on its way out. Records name their host before
// admission, so a live session's guest always has one.
func orphanedGuest(rec schema.Scratch, found bool, host string) bool {
	return !found || rec.Host != host || rec.State == schema.ScratchDeleted || rec.State == schema.ScratchDeleting
}

// deleteHost retires then deletes a host, so racing creates stop choosing it
// first. A host that took a session meanwhile keeps its stamp and is
// collected once it drains.
//
// TODO: Drain the host through its broker before the delete. A create that
// listed the host before its retirement can still admit and lose its
// session.
func deleteHost(ctx context.Context, deps *ScratchReapDeps, host Instance, now time.Time) bool {
	if err := retire(ctx, deps.GCE, host, now); err != nil {
		log.Printf("reap: retiring host %s: %v", host.Name, err)
		return false
	}
	if list, err := deps.Hosts.Guests(ctx, host); err == nil && len(list.Guests) > 0 {
		log.Printf("reap: host %s took a session during retirement; leaving it to drain", host.Name)
		return false
	}
	if err := deps.GCE.DeleteInstance(ctx, host.Zone, host.Name); err != nil {
		log.Printf("reap: deleting host %s: %v", host.Name, err)
		return false
	}
	return true
}

// hostReachable probes the broker for hostHealthTimeout. A cancelled pass
// counts as reachable, so it retires nothing.
func hostReachable(ctx context.Context, deps *ScratchReapDeps, host Instance) bool {
	err := pollHealthy(ctx, deps.hostHealthTimeout(), deps.hostHealthDelay(), func(ctx context.Context) error {
		return deps.Hosts.Healthz(ctx, host)
	})
	if err == nil || errors.Is(err, context.Canceled) {
		return true
	}
	log.Printf("reap: host %s healthz: %v", host.Name, err)
	return false
}
