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

// ScratchCreateDeps wires ScratchCreate.
type ScratchCreateDeps struct {
	Scratches db.Scratch
	GCE       GCE
	// Standard is the required class config for MachineClassStandard.
	Standard ClassConfig
	// Jumbo is the optional class config for MachineClassJumbo. nil
	// means jumbo isn't available and requsts returns InvalidArgument.
	Jumbo *ClassConfig
	Zone  string
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
//	InternalIP -> UpdateState(Ready) -> return.
//
// Ready currently means "VM exists." The template supplies the boot disk
// and local SSD partitions; no additional disks are attached.
// TODO: Poll a worker /healthz before UpdateState(Ready) so Ready means
// "worker is reachable" once the worker service exists.
//
// If any step fails after resources are created, best-effort teardown is
// run and the record is marked Deleted (records persist for audit).
func ScratchCreate(ctx context.Context, req schema.ScratchCreateRequest, deps *ScratchCreateDeps) (*schema.Scratch, error) {
	class, err := selectClass(req.MachineClass, deps.Standard, deps.Jumbo)
	if err != nil {
		return nil, api.AsStatus(codes.InvalidArgument, err)
	}

	scratchID := mintID(deps.IDGen)
	now := time.Now().UTC()
	scratch := schema.Scratch{
		ID:           scratchID,
		BuildID:      req.BuildID,
		MachineClass: req.MachineClass,
		Zone:         deps.Zone,
		VMName:       "scratch-" + scratchID,
		State:        schema.ScratchStarting,
		Created:      now,
		Updated:      now,
	}
	if err := deps.Scratches.Insert(ctx, scratch); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches insert"))
	}

	// Best-effort cleanup on any failure past this point: attempt deletion of
	// GCE resources we may have created and mark the record Deleted.
	ranInsertInstance := false
	cleanup := func() {
		bg := context.Background()
		if ranInsertInstance {
			if err := deps.GCE.DeleteInstance(bg, scratch.Zone, scratch.VMName); err != nil {
				log.Printf("teardown DeleteInstance(%s): %v", scratch.VMName, err)
			}
		}
		if err := deps.Scratches.UpdateState(bg, scratchID, schema.ScratchDeleted); err != nil {
			log.Printf("teardown UpdateState(%s, Deleted): %v", scratchID, err)
		}
	}

	ranInsertInstance = true
	inst, err := deps.GCE.InsertInstanceFromTemplate(ctx, scratch.Zone, scratch.VMName, class.InstanceTemplate, nil)
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

func mintID(gen func() string) string {
	if gen != nil {
		return gen()
	}
	return uuid.New().String()
}
