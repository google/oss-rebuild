// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// ScratchTopUpRequest is the scheduler's empty request.
type ScratchTopUpRequest struct{}

func (ScratchTopUpRequest) Validate() error { return nil }

// ScratchTopUpResponse reports the hosts one pass minted.
type ScratchTopUpResponse struct {
	HostsMinted int `json:"hosts_minted"`
}

// ScratchTopUp creates host VMs until each class has Headroom free guest
// slots on hosts of its current template. It returns once GCE has created the
// VMs, before they can take guests. The reaper deletes any that stay empty or
// never start.
func ScratchTopUp(ctx context.Context, _ ScratchTopUpRequest, deps *ScratchCreateDeps) (*ScratchTopUpResponse, error) {
	classes := map[schema.MachineClass]ClassConfig{schema.MachineClassStandard: deps.Standard}
	if deps.Jumbo != nil {
		classes[schema.MachineClassJumbo] = *deps.Jumbo
	}
	resp := &ScratchTopUpResponse{}
	for mc, class := range classes {
		if class.Headroom == 0 || class.Slots == 0 {
			continue
		}
		labels := hostLabels(mc, class.InstanceTemplate)
		free := freeSlots(ctx, deps.Hosts, listHosts(ctx, deps.GCE, deps.Zones, labels), time.Now().UTC(), class.Slots)
		need := max(0, (class.Headroom-free+class.Slots-1)/class.Slots)
		var minted atomic.Int64
		runConcurrently(ctx, make([]struct{}, need), need, func(ctx context.Context, _ struct{}) {
			if _, err := insertHost(ctx, deps, class, labels); err != nil {
				log.Printf("top-up %s: %v", mc, err)
				return
			}
			minted.Add(1)
		})
		resp.HostsMinted += int(minted.Load())
	}
	return resp, nil
}

// bootWindow is how long a new host counts as starting up.
// NOTE: The top-up replaces a host still starting after this (they take ~30 s).
const bootWindow = 90 * time.Second

// freeSlots counts the free guest slots on listed hosts. A host that cannot
// report its guests yet counts all of its slots as free, even when a create
// minted it and will take one.
func freeSlots(ctx context.Context, hosts *HostClient, listed []Instance, now time.Time, slots int) int {
	listed = slices.DeleteFunc(listed, func(h Instance) bool { return !admissible(h, now, hostMaxAge) })
	free := 0
	for host, list := range guests(ctx, hosts, listed) {
		switch {
		case list.Slots > 0:
			free += max(0, list.Slots-len(list.Guests))
		case !olderThan(host.Created, now, bootWindow):
			free += slots
		}
	}
	return free
}
