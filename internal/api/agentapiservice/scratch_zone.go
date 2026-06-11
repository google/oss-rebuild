// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// ZoneCooldown skips zones recently observed as stockout-exhausted so
// subsequent ScratchCreate calls bypass them until the TTL expires.
// Per-replica state: a latency hint, not a correctness barrier (the
// fallthrough loop still hits everything once entries expire).
type ZoneCooldown struct {
	ttl   time.Duration
	mu    sync.Mutex
	until map[string]time.Time
}

// NewZoneCooldown returns a tracker that skips marked zones for ttl.
// ttl <= 0 falls back to DefaultZoneCooldownTTL.
func NewZoneCooldown(ttl time.Duration) *ZoneCooldown {
	if ttl <= 0 {
		ttl = DefaultZoneCooldownTTL
	}
	return &ZoneCooldown{ttl: ttl, until: map[string]time.Time{}}
}

// mark records zone as exhausted for the configured TTL.
func (c *ZoneCooldown) mark(zone string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until == nil {
		c.until = map[string]time.Time{}
	}
	c.until[zone] = time.Now().Add(c.ttl)
}

// active returns zones not currently cooling down, in input order;
// expired entries are pruned.
func (c *ZoneCooldown) active(zones []string) []string {
	if c == nil {
		return zones
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		exp, ok := c.until[z]
		if !ok {
			out = append(out, z)
			continue
		}
		if !now.Before(exp) {
			delete(c.until, z)
			out = append(out, z)
			continue
		}
		// still cooling down; skip
	}
	return out
}

// DefaultZoneCooldownTTL is how long a zone is skipped after a stockout.
const DefaultZoneCooldownTTL = 5 * time.Minute

// insertWithFallthrough creates an instance in the first zone that
// accepts it, marking stockout failures in cooldown and continuing.
// Non-stockout errors (auth, invalid template, ctx cancel, network)
// bubble up without trying additional zones.
func insertWithFallthrough(
	ctx context.Context,
	gce GCE,
	zones []string,
	cooldown *ZoneCooldown,
	name, templateURL string,
) (inst Instance, zone string, err error) {
	if len(zones) == 0 {
		return Instance{}, "", errors.New("no zones configured")
	}
	candidates := cooldown.active(zones)
	if len(candidates) == 0 {
		// Everything cooled down; try the full list anyway.
		candidates = zones
	}
	var lastErr error
	for _, z := range candidates {
		inst, err := gce.InsertInstanceFromTemplate(ctx, z, name, templateURL, nil)
		if err == nil {
			return inst, z, nil
		}
		if isZoneExhausted(err) {
			log.Printf("zone %s exhausted (%v); marking cooldown", z, err)
			cooldown.mark(z)
			lastErr = err
			continue
		}
		return Instance{}, z, err
	}
	return Instance{}, "", errors.Wrap(lastErr, "all zones exhausted")
}
