// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"slices"
	"sort"
	"time"
)

// Caps bound one dispatch pass. Zero means unbounded and CapNone admits none.
type Caps struct {
	Batch int // dispatches of any stage
	Build int // dispatches at the build stages
	Agent int // dispatches at StageAgent, the rationed stage
}

// CapNone is the cap of a pool with no room left.
const CapNone = -1

// full reports whether used dispatches exhaust limit.
func full(limit, used int) bool {
	return limit < 0 || (limit > 0 && used >= limit)
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
// that fits under caps. A campaign over its stage's cap is passed over rather
// than ending the pass, so the other stage's campaigns behind it still run. A
// campaign whose NotBefore is still ahead is passed over the same way.
func Select(queued []Campaign, caps Caps, now time.Time, k, tauHours float64) []Campaign {
	var out []Campaign
	var builds, agents int
	for _, c := range Order(queued, now, k, tauHours) {
		if caps.Batch > 0 && len(out) >= caps.Batch {
			break
		}
		if now.Before(c.NotBefore) {
			continue
		}
		if c.Stage == StageAgent {
			if full(caps.Agent, agents) {
				continue
			}
			agents++
		} else {
			if full(caps.Build, builds) {
				continue
			}
			builds++
		}
		out = append(out, c)
	}
	return out
}
