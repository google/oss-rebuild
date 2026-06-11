// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	compute "google.golang.org/api/compute/v0.beta"
	"google.golang.org/api/googleapi"
)

// opError wraps a terminal zone-op error so callers can classify it
// by Code (e.g., ZONE_RESOURCE_POOL_EXHAUSTED) via errors.As.
type opError struct {
	OpName  string
	Code    string
	Message string
}

func (e *opError) Error() string {
	return fmt.Sprintf("op %s failed: %s: %s", e.OpName, e.Code, e.Message)
}

// zoneExhaustedCodes are GCE error codes that trigger zone fall-through.
// QUOTA_EXCEEDED is included because CPU quota is typically zone-scoped;
// project-wide quotas fail everywhere anyway, so the extra attempt is cheap.
var zoneExhaustedCodes = map[string]bool{
	"ZONE_RESOURCE_POOL_EXHAUSTED":              true,
	"ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS": true,
	"QUOTA_EXCEEDED":                            true,
}

// isZoneExhausted reports whether err is a stockout or per-zone quota
// from Compute Engine. Inspects both *googleapi.Error (immediate Insert
// failure) and *opError (polled zone-op terminal failure).
func isZoneExhausted(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		for _, it := range gerr.Errors {
			if zoneExhaustedCodes[it.Reason] {
				return true
			}
		}
	}
	var oerr *opError
	if errors.As(err, &oerr) {
		if zoneExhaustedCodes[oerr.Code] {
			return true
		}
	}
	return false
}

// Instance summarizes the fields the broker reads back from Compute Engine
// after creating or looking up an instance.
type Instance struct {
	Name       string
	Zone       string
	InternalIP string
}

// GCE abstracts the Compute Engine operations the broker uses for env
// lifecycle. Kept narrow so a memory fake (MemoryGCE) covers the surface
// for tests, and a real Compute-backed impl can be wired at binary boot
// without leaking SDK types upward.
type GCE interface {
	// InsertInstanceFromTemplate creates a VM with the given labels and
	// returns its summary (including InternalIP) once it is observable to
	// the API. labels may be nil for ad-hoc instances; pool replenishment
	// passes pool labels so ListStoppedInstances can find them later.
	InsertInstanceFromTemplate(ctx context.Context, zone, name, templateURL string, labels map[string]string) (Instance, error)
	DeleteInstance(ctx context.Context, zone, name string) error
	// ListStoppedInstances returns terminated VMs whose labels match every
	// entry in labels (subset match).
	ListStoppedInstances(ctx context.Context, zone string, labels map[string]string) ([]Instance, error)
	// StartInstance brings a terminated VM up and returns its summary
	// (including the (possibly new) InternalIP).
	StartInstance(ctx context.Context, zone, name string) (Instance, error)
	// StopInstance terminates a running VM (used at replenishment to
	// return a freshly-inserted VM to stopped state).
	StopInstance(ctx context.Context, zone, name string) error
}

// computeGCE implements GCE against google.golang.org/api/compute.
// Each call submits the underlying mutation and blocks polling
// ZoneOperations.Get until the GCE operation reaches DONE so the broker
// can treat the call as synchronous.
type computeGCE struct {
	svc     *compute.Service
	project string
	// OpPollInterval and OpTimeout bound the zone-op polling loop.
	// Defaults are sensible production values; tests can override.
	OpPollInterval time.Duration
	OpTimeout      time.Duration
}

// NewComputeGCE returns a GCE backed by the Compute v1 REST service for
// project. Uses Application Default Credentials.
func NewComputeGCE(ctx context.Context, project string) (GCE, error) {
	svc, err := compute.NewService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "compute service")
	}
	return &computeGCE{
		svc:            svc,
		project:        project,
		OpPollInterval: 2 * time.Second,
		OpTimeout:      2 * time.Minute,
	}, nil
}

func (g *computeGCE) InsertInstanceFromTemplate(ctx context.Context, zone, name, templateURL string, labels map[string]string) (Instance, error) {
	op, err := g.svc.Instances.Insert(g.project, zone, &compute.Instance{
		Name:   name,
		Labels: labels,
		Scheduling: &compute.Scheduling{
			// Cuts the wait on delete by ~90s. Requires v0.beta API.
			SkipGuestOsShutdown: true,
		},
	}).
		SourceInstanceTemplate(templateURL).
		Context(ctx).Do()
	if err != nil {
		return Instance{}, errors.Wrap(err, "instances.insert")
	}
	if err := g.waitZoneOp(ctx, zone, op); err != nil {
		return Instance{}, err
	}
	inst, err := g.svc.Instances.Get(g.project, zone, name).Context(ctx).Do()
	if err != nil {
		return Instance{}, errors.Wrap(err, "instances.get")
	}
	return Instance{Name: inst.Name, Zone: zone, InternalIP: instanceInternalIP(inst)}, nil
}

func (g *computeGCE) ListStoppedInstances(ctx context.Context, zone string, labels map[string]string) ([]Instance, error) {
	var filter strings.Builder
	filter.WriteString("status = TERMINATED")
	for k, v := range labels {
		fmt.Fprintf(&filter, " AND labels.%s = %s", k, v)
	}
	call := g.svc.Instances.List(g.project, zone).Filter(filter.String()).Context(ctx)
	var out []Instance
	if err := call.Pages(ctx, func(page *compute.InstanceList) error {
		for _, inst := range page.Items {
			out = append(out, Instance{
				Name: inst.Name, Zone: zone, InternalIP: instanceInternalIP(inst),
			})
		}
		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "instances.list")
	}
	return out, nil
}

func (g *computeGCE) StartInstance(ctx context.Context, zone, name string) (Instance, error) {
	op, err := g.svc.Instances.Start(g.project, zone, name).Context(ctx).Do()
	if err != nil {
		return Instance{}, errors.Wrap(err, "instances.start")
	}
	if err := g.waitZoneOp(ctx, zone, op); err != nil {
		return Instance{}, err
	}
	inst, err := g.svc.Instances.Get(g.project, zone, name).Context(ctx).Do()
	if err != nil {
		return Instance{}, errors.Wrap(err, "instances.get")
	}
	return Instance{Name: inst.Name, Zone: zone, InternalIP: instanceInternalIP(inst)}, nil
}

func (g *computeGCE) StopInstance(ctx context.Context, zone, name string) error {
	op, err := g.svc.Instances.Stop(g.project, zone, name).Context(ctx).Do()
	if err != nil {
		return errors.Wrap(err, "instances.stop")
	}
	return g.waitZoneOp(ctx, zone, op)
}

// DeleteInstance is idempotent: deleting a missing instance succeeds, so
// retried teardowns converge after a partially-observed earlier delete.
func (g *computeGCE) DeleteInstance(ctx context.Context, zone, name string) error {
	op, err := g.svc.Instances.Delete(g.project, zone, name).Context(ctx).Do()
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "instances.delete")
	}
	return g.waitZoneOp(ctx, zone, op)
}

func isNotFound(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == 404
}

func (g *computeGCE) waitZoneOp(ctx context.Context, zone string, op *compute.Operation) error {
	ctx, cancel := context.WithTimeout(ctx, g.OpTimeout)
	defer cancel()
	for {
		if op.Status == "DONE" {
			if op.Error != nil && len(op.Error.Errors) > 0 {
				e := op.Error.Errors[0]
				return &opError{OpName: op.Name, Code: e.Code, Message: e.Message}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.OpPollInterval):
		}
		next, err := g.svc.ZoneOperations.Get(g.project, zone, op.Name).Context(ctx).Do()
		if err != nil {
			return errors.Wrap(err, "polling zone op")
		}
		op = next
	}
}

func instanceInternalIP(inst *compute.Instance) string {
	for _, ni := range inst.NetworkInterfaces {
		if ni.NetworkIP != "" {
			return ni.NetworkIP
		}
	}
	return ""
}
