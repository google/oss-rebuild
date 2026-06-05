// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// ClassConfig is the per-machine-class GCE shape used by ScratchCreate.
type ClassConfig struct {
	// InstanceTemplate is the full Compute Engine resource name of the
	// instance template to clone from (e.g.
	// projects/foo/global/instanceTemplates/builder-standard). The template
	// supplies the boot disk and any local SSD partitions used as scratch.
	InstanceTemplate string
}

// selectClass returns the ClassConfig for the given machine class.
// Jumbo is optional (nil means "jumbo not configured for this
// deployment"); standard is always required.
func selectClass(class schema.MachineClass, standard ClassConfig, jumbo *ClassConfig) (ClassConfig, error) {
	switch class {
	case schema.MachineClassStandard:
		return standard, nil
	case schema.MachineClassJumbo:
		if jumbo == nil {
			return ClassConfig{}, errors.Errorf("machine_class %q not configured", class)
		}
		return *jumbo, nil
	default:
		return ClassConfig{}, errors.Errorf("unknown machine_class %q", class)
	}
}

// HealthProbe pings the worker at ip and returns nil when /healthz responds.
// Injected so tests can drive the polling deterministically.
//
// /healthz is a plain unauthenticated GET (outside the act framework) so the
// broker can probe before any caller context exists and so external health
// checkers (LB, MIG) can hit it without minting tokens. Could be folded into
// act if those constraints stop mattering.
type HealthProbe func(ctx context.Context, internalIP string) error

// ScratchCreateDeps wires ScratchCreate.
type ScratchCreateDeps struct {
	Scratches db.Scratch
	GCE       GCE
	// Standard is the required class config for MachineClassStandard.
	Standard ClassConfig
	// Jumbo is the optional class config for MachineClassJumbo. nil
	// means jumbo isn't available and requsts returns InvalidArgument.
	Jumbo *ClassConfig
	// Zones is the ordered list of GCE zones to try for instance
	// creation. The first listed zone is preferred; subsequent zones
	// are used only when an earlier zone returns a stockout / quota
	// error (see isZoneExhausted). Cross-region failover is expressed
	// by including zones from multiple regions in the list.
	Zones []string
	// Cooldown skips zones recently observed to be exhausted so we don't
	// pay an extra round-trip on each request during sustained outages.
	// Must be a singleton shared across requests (the binary, not deps,
	// owns its lifecycle); nil disables the cool-down.
	Cooldown *ZoneCooldown
	// HealthProbe is called until it returns nil or HealthTimeout elapses.
	HealthProbe HealthProbe
	// HealthTimeout bounds the polling loop. Zero means 90 seconds.
	HealthTimeout time.Duration
	// HealthInterval is the gap between probe attempts. Zero means 500ms.
	HealthInterval time.Duration
	// IDGen mints scratch IDs. nil falls back to uuid.New().String().
	IDGen func() string
}

// ScratchGetDeps wires ScratchGet.
type ScratchGetDeps struct {
	Scratches db.Scratch
}

// ScratchDeleteDeps wires ScratchDelete.
type ScratchDeleteDeps struct {
	Scratches db.Scratch
	GCE       GCE
}

// ScratchCreate provisions a new build environment synchronously. Steps:
//
//	Scratches.Insert(state=Starting) -> InsertInstance -> Update with
//	InternalIP -> poll HealthProbe -> UpdateState(Ready) -> return.
//
// If any step fails after resources are created, best-effort teardown is
// run and the record is marked Deleted (records persist for audit).
func ScratchCreate(ctx context.Context, req schema.ScratchCreateRequest, deps *ScratchCreateDeps) (*schema.Scratch, error) {
	class, err := selectClass(req.MachineClass, deps.Standard, deps.Jumbo)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, err)
	}

	scratchID := mintID(deps.IDGen)
	obliviousID := uuid.New().String() // Used in GCS object paths
	now := time.Now().UTC()
	scratch := schema.Scratch{
		ID:           scratchID,
		BuildID:      req.BuildID,
		ObliviousID:  obliviousID,
		MachineClass: req.MachineClass,
		VMName:       "scratch-" + scratchID,
		State:        schema.ScratchStarting,
		Created:      now,
		Updated:      now,
		// Zone is set after the fallthrough loop is able to allocate.
	}
	if err := deps.Scratches.Insert(ctx, scratch); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches insert"))
	}

	// Best-effort teardown on failure past this point. scratch.Zone gates
	// DeleteInstance: insertWithFallthrough sets it whenever a VM may
	// exist (success, or non-stockout orphan), and leaves it "" when GCE
	// semantics guarantee none (all-stockouts).
	cleanup := func() {
		bg := context.Background()
		if scratch.Zone != "" {
			if err := deps.GCE.DeleteInstance(bg, scratch.Zone, scratch.VMName); err != nil {
				log.Printf("teardown DeleteInstance(%s/%s): %v", scratch.Zone, scratch.VMName, err)
			}
		}
		if err := deps.Scratches.UpdateState(bg, scratchID, schema.ScratchDeleted); err != nil {
			log.Printf("teardown UpdateState(%s, Deleted): %v", scratchID, err)
		}
	}

	inst, cleanupZone, err := insertWithFallthrough(ctx, deps.GCE, deps.Zones, deps.Cooldown, scratch.VMName, class.InstanceTemplate)
	scratch.Zone = cleanupZone
	if err != nil {
		cleanup()
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "insert instance"))
	}

	scratch.InternalIP = inst.InternalIP
	scratch.Updated = time.Now().UTC()
	// Seed LastUsed at provisioning so the reaper's ListIdleSince doesn't
	// pick up a brand-new scratch whose zero-time LastUsed satisfies the
	// "last_used < cutoff" filter.
	scratch.LastUsed = scratch.Updated
	if err := deps.Scratches.Update(ctx, scratch); err != nil {
		cleanup()
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches update with IP"))
	}

	if err := waitHealthy(ctx, deps, scratch.InternalIP); err != nil {
		cleanup()
		return nil, api.AsStatus(codes.DeadlineExceeded, errors.Wrap(err, "worker healthz"))
	}

	if err := deps.Scratches.UpdateState(ctx, scratchID, schema.ScratchReady); err != nil {
		cleanup()
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches update state ready"))
	}
	scratch.State = schema.ScratchReady
	return &scratch, nil
}

// ScratchGet returns the scratch record by ID.
func ScratchGet(ctx context.Context, req schema.ScratchGetRequest, deps *ScratchGetDeps) (*schema.Scratch, error) {
	scratch, err := deps.Scratches.Get(ctx, req.ScratchID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", req.ScratchID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}
	return &scratch, nil
}

// ScratchDelete tears down the GCE resources and records the scratch as
// Deleted. The record itself is preserved for audit (a separate retention
// sweep can later hard-delete via Scratches.Delete).
func ScratchDelete(ctx context.Context, req schema.ScratchDeleteRequest, deps *ScratchDeleteDeps) (*schema.ScratchDeleteResponse, error) {
	scratch, err := deps.Scratches.Get(ctx, req.ScratchID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", req.ScratchID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}

	if err := deps.Scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleting); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches update state deleting"))
	}
	if scratch.VMName != "" {
		if err := deps.GCE.DeleteInstance(ctx, scratch.Zone, scratch.VMName); err != nil {
			log.Printf("DeleteInstance(%s): %v", scratch.VMName, err)
		}
	}
	if err := deps.Scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleted); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches update state deleted"))
	}
	return &schema.ScratchDeleteResponse{ScratchID: scratch.ID, State: schema.ScratchDeleted}, nil
}

func waitHealthy(ctx context.Context, deps *ScratchCreateDeps, ip string) error {
	timeout := deps.HealthTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	interval := deps.HealthInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := deps.HealthProbe(probeCtx, ip); err == nil {
		return nil
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-probeCtx.Done():
			return probeCtx.Err()
		case <-t.C:
			if err := deps.HealthProbe(probeCtx, ip); err == nil {
				return nil
			}
		}
	}
}

func mintID(gen func() string) string {
	if gen != nil {
		return gen()
	}
	return uuid.New().String()
}
