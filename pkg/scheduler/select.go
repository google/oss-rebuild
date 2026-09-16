// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"slices"
	"sort"
	"time"
)

// Caps bound one dispatch pass. Zero means unbounded.
type Caps struct {
	Batch int // dispatches of any stage
	Agent int // dispatches at StageAgent, the rationed stage
}

// Order sorts campaigns by DispatchOrder as of now, highest first, ties
// newest first. Admission and dispatch both rank by it.
func Order(cs []Campaign, now time.Time, k, tauHours float64) []Campaign {
	out := slices.Clone(cs)
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := out[i].DispatchOrder(now, k, tauHours), out[j].DispatchOrder(now, k, tauHours)
		if oi != oj {
			return oi > oj
		}
		return out[i].Published.After(out[j].Published)
	})
	return out
}

// Select returns the queued campaigns one pass dispatches: the ordered prefix
// that fits under caps. An agent campaign over the agent cap is passed over
// rather than ending the pass, so cheaper stages behind it still run.
func Select(queued []Campaign, caps Caps, now time.Time, k, tauHours float64) []Campaign {
	var out []Campaign
	var agents int
	for _, c := range Order(queued, now, k, tauHours) {
		if caps.Batch > 0 && len(out) >= caps.Batch {
			break
		}
		if c.Stage == StageAgent {
			if caps.Agent > 0 && agents >= caps.Agent {
				continue
			}
			agents++
		}
		out = append(out, c)
	}
	return out
}
