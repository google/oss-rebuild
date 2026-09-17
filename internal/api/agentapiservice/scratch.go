// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
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
// If any step fails after the record is written, deleteScratch runs as
// best-effort cleanup.
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

	// insertWithFallthrough sets scratch.Zone whenever a VM may exist and
	// leaves it "" when none can (every zone stocked out), so cleanup
	// needs no zone sweep.
	cleanup := func() {
		if err := deleteScratch(context.Background(), deps.Scratches, deps.GCE, scratch, nil); err != nil {
			log.Printf("scratch %s teardown: %v", scratchID, err)
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

// ScratchDelete tears down the scratch's VM and records it as Deleted.
// The record is kept for audit. A failed VM delete leaves it Deleting for
// the reaper to retry.
func ScratchDelete(ctx context.Context, req schema.ScratchDeleteRequest, deps *ScratchDeleteDeps) (*schema.ScratchDeleteResponse, error) {
	scratch, err := deps.Scratches.Get(ctx, req.ScratchID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", req.ScratchID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}
	if err := deleteScratch(ctx, deps.Scratches, deps.GCE, scratch, nil); err != nil {
		return nil, api.AsStatus(codes.Internal, err)
	}
	return &schema.ScratchDeleteResponse{ScratchID: scratch.ID, State: schema.ScratchDeleted}, nil
}

// deleteScratch is the only path into Deleting and Deleted. The record is
// marked Deleting, the VM is deleted, and only once the VM is confirmed
// gone does the record advance to Deleted, so any failure leaves it
// Deleting for the reaper's stuck sweep. The Deleting write bumps Updated,
// which keeps the record out of that sweep for IdleThreshold while this
// delete runs.
//
// zones is where the VM may sit when the record carries no zone, which
// only a create that died before persisting placement produces. Callers
// whose record has a zone, or never had a VM, pass nil.
func deleteScratch(ctx context.Context, scratches db.Scratch, gce GCE, scratch schema.Scratch, zones []string) error {
	if scratch.State != schema.ScratchDeleting {
		if err := scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleting); err != nil {
			return errors.Wrap(err, "marking deleting")
		}
	}
	if scratch.Zone != "" {
		zones = []string{scratch.Zone}
	}
	if scratch.VMName != "" {
		for _, zone := range zones {
			if err := gce.DeleteInstance(ctx, zone, scratch.VMName); err != nil {
				return errors.Wrapf(err, "deleting instance %s/%s", zone, scratch.VMName)
			}
		}
	}
	if err := scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleted); err != nil {
		return errors.Wrap(err, "marking deleted")
	}
	return nil
}

func waitHealthy(ctx context.Context, deps *ScratchCreateDeps, ip string) error {
	timeout := deps.HealthTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
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
