// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"path"
	"sync"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// ZoneCooldown skips zones recently observed as stockout-exhausted for a
// host template so subsequent ScratchCreate calls bypass them until the TTL
// expires. Stockouts are per machine family, so a zone exhausted for one
// template may still have capacity for others. The state is per replica and
// only saves latency, since the fallthrough loop tries every zone again once
// entries expire.
type ZoneCooldown struct {
	ttl   time.Duration
	mu    sync.Mutex
	until map[cooldownKey]time.Time
}

type cooldownKey struct{ template, zone string }

// NewZoneCooldown returns a tracker that skips marked zones for ttl.
// ttl <= 0 falls back to DefaultZoneCooldownTTL.
func NewZoneCooldown(ttl time.Duration) *ZoneCooldown {
	if ttl <= 0 {
		ttl = DefaultZoneCooldownTTL
	}
	return &ZoneCooldown{ttl: ttl, until: map[cooldownKey]time.Time{}}
}

// mark records zone as exhausted for template for the configured TTL.
func (c *ZoneCooldown) mark(template, zone string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until == nil {
		c.until = map[cooldownKey]time.Time{}
	}
	c.until[cooldownKey{template, zone}] = time.Now().Add(c.ttl)
}

// active returns the zones not currently cooling down for template, in
// input order; expired entries are pruned.
func (c *ZoneCooldown) active(template string, zones []string) []string {
	if c == nil {
		return zones
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		k := cooldownKey{template, z}
		exp, ok := c.until[k]
		if !ok {
			out = append(out, z)
			continue
		}
		if !now.Before(exp) {
			delete(c.until, k)
			out = append(out, z)
			continue
		}
		// still cooling down; skip
	}
	return out
}

// DefaultZoneCooldownTTL is how long a zone is skipped after a stockout.
const DefaultZoneCooldownTTL = 5 * time.Minute

// errCapacity reports that every candidate zone refused the instance for
// stock or quota under every template, so none will take it until
// something frees up.
var errCapacity = errors.New("all zones exhausted")

// insertWithFallthrough creates an instance from the first of templates
// that some zone accepts, trying each template's zones in order and marking
// stockout failures in cooldown. A template with every zone cooling down is
// skipped, and if that leaves none, all are tried.
// Non-stockout errors (auth, invalid template, ctx cancel, network) bubble
// up without trying additional zones or templates.
func insertWithFallthrough(
	ctx context.Context,
	gce GCE,
	zones []string,
	cooldown *ZoneCooldown,
	name string,
	class schema.MachineClass,
	templates []string,
) (inst Instance, zone string, err error) {
	if len(zones) == 0 {
		return Instance{}, "", errors.New("no zones configured")
	}
	if len(templates) == 0 {
		return Instance{}, "", errors.New("no host templates configured")
	}
	type sweep struct {
		template string
		zones    []string
	}
	var sweeps []sweep
	for _, t := range templates {
		if zs := cooldown.active(t, zones); len(zs) > 0 {
			sweeps = append(sweeps, sweep{t, zs})
		}
	}
	if len(sweeps) == 0 {
		// Everything cooled down; try the full list anyway.
		for _, t := range templates {
			sweeps = append(sweeps, sweep{t, zones})
		}
	}
	var lastErr error
	for _, sw := range sweeps {
		for _, z := range sw.zones {
			inst, err := gce.InsertInstanceFromTemplate(ctx, z, name, sw.template, hostLabels(class, sw.template))
			if err == nil {
				return inst, z, nil
			}
			if isZoneExhausted(err) {
				log.Printf("zone %s exhausted for %s (%v); marking cooldown", z, path.Base(sw.template), err)
				cooldown.mark(sw.template, z)
				lastErr = err
				continue
			}
			return Instance{}, z, err
		}
	}
	return Instance{}, "", errors.Wrap(errCapacity, lastErr.Error())
}
