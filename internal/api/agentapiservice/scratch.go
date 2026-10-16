// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// ClassConfig is the per-machine-class scratch host shape used by
// ScratchCreate.
type ClassConfig struct {
	InstanceTemplate string // full name of the host template
}

// selectClass returns the host class serving a machine class. Jumbo is
// optional and standard required.
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
	Scratches         db.Scratch
	GCE               GCE
	Standard          ClassConfig   // required, and serves micro too
	Jumbo             *ClassConfig  // optional. nil refuses jumbo
	Hosts             *HostClient   // required
	HostHealthTimeout time.Duration // new host wait. Zero means 10m
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
	Cooldown      *ZoneCooldown
	HealthTimeout time.Duration // guest wait. Zero means 90s
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
	Hosts     *HostClient // required
	GCE       GCE         // per-VM-era records, optional
}

// ScratchCreate provisions a build environment synchronously: insert the
// record as Starting, admit the session on a host (admitAnywhere), wait for
// its guest, and mark it Ready. A failure after the insert cleans up with
// deleteScratch.
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
		State:        schema.ScratchStarting,
		Created:      now,
		Updated:      now,
		// place writes Host, Zone and InternalIP before each admission.
	}
	if err := deps.Scratches.Insert(ctx, scratch); err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches insert"))
	}
	// Best-effort teardown on failure. A host that refused the session answers
	// its release with NotFound.
	cleanup := func() {
		if err := deleteScratch(context.Background(), deps.Scratches, deps.GCE, deps.Hosts, scratch, nil); err != nil {
			log.Printf("scratch %s teardown: %v", scratchID, err)
		}
	}
	host, err := admitAnywhere(ctx, deps, &scratch, class)
	if err != nil {
		cleanup()
		if errors.Is(err, errCapacity) {
			return nil, api.AsStatus(codes.ResourceExhausted, errors.Wrap(err, "admitting guest"))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "admitting guest"))
	}
	// The session is ready once its guest's sshd answers.
	if err := pollHealthy(ctx, deps.healthTimeout(), deps.healthInterval(), func(ctx context.Context) error {
		return deps.Hosts.GuestHealthz(ctx, host, scratchID)
	}); err != nil {
		cleanup()
		if errors.Is(err, ErrGuestExited) {
			return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "guest healthz"))
		}
		return nil, api.AsStatus(codes.DeadlineExceeded, errors.Wrap(err, "guest healthz"))
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

// ScratchDelete releases the scratch's guest, or deletes a per-VM-era
// record's VM, and marks it Deleted, keeping the record for audit. A failed
// VM delete leaves it Deleting for the reaper.
func ScratchDelete(ctx context.Context, req schema.ScratchDeleteRequest, deps *ScratchDeleteDeps) (*schema.ScratchDeleteResponse, error) {
	scratch, err := deps.Scratches.Get(ctx, req.ScratchID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.AsStatus(codes.NotFound, errors.Errorf("scratch %q not found", req.ScratchID))
		}
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "scratches get"))
	}
	if err := deleteScratch(ctx, deps.Scratches, deps.GCE, deps.Hosts, scratch, nil); err != nil {
		return nil, api.AsStatus(codes.Internal, err)
	}
	return &schema.ScratchDeleteResponse{ScratchID: scratch.ID, State: schema.ScratchDeleted}, nil
}

// deleteScratch is the only path to Deleting and Deleted. It marks the
// record Deleting, deletes a per-VM-era record's VM or releases a host-era
// record's guest, then marks it Deleted. A failed VM delete stays Deleting
// for the reaper's stuck sweep. A failed guest release advances anyway,
// since the host sweep destroys guests of Deleting and Deleted records.
// Marking Deleting bumps Updated, which keeps the stuck sweep away while
// this runs.
//
// zones is where to look for a VM when the record has no zone. gce may be
// nil for callers that never meet per-VM-era records.
func deleteScratch(ctx context.Context, scratches db.Scratch, gce GCE, hosts *HostClient, scratch schema.Scratch, zones []string) error {
	if err := scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleting); err != nil {
		return errors.Wrap(err, "marking deleting")
	}
	if scratch.Zone != "" {
		zones = []string{scratch.Zone}
	}
	if scratch.VMName != "" {
		if gce == nil {
			return errors.Errorf("scratch %s names VM %s but no GCE client is configured", scratch.ID, scratch.VMName)
		}
		for _, zone := range zones {
			if err := gce.DeleteInstance(ctx, zone, scratch.VMName); err != nil {
				return errors.Wrapf(err, "deleting instance %s/%s", zone, scratch.VMName)
			}
		}
	}
	// A failed release is only logged: sweepHosts destroys guests whose record
	// is Deleting or Deleted.
	if err := releaseScratch(ctx, hosts, scratch); err != nil {
		log.Printf("releasing scratch %s: %v", scratch.ID, err)
	}
	if err := scratches.UpdateState(ctx, scratch.ID, schema.ScratchDeleted); err != nil {
		return errors.Wrap(err, "marking deleted")
	}
	return nil
}

func (d *ScratchCreateDeps) healthTimeout() time.Duration {
	return cmp.Or(d.HealthTimeout, 90*time.Second)
}

// hostHealthTimeout is minutes: a host fetches its image before the broker listens.
func (d *ScratchCreateDeps) hostHealthTimeout() time.Duration {
	return cmp.Or(d.HostHealthTimeout, 10*time.Minute)
}

func (d *ScratchCreateDeps) healthInterval() time.Duration {
	return cmp.Or(d.HealthInterval, 500*time.Millisecond)
}

// probeTimeout bounds one health probe, so a blackholed target costs one
// interval rather than the kernel's connect retries.
const probeTimeout = 10 * time.Second

// pollHealthy calls probe until it succeeds or timeout elapses.
func pollHealthy(ctx context.Context, timeout, interval time.Duration, probe func(context.Context) error) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	try := func() error {
		ctx, cancel := context.WithTimeout(probeCtx, probeTimeout)
		defer cancel()
		return probe(ctx)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := try(); err == nil || errors.Is(err, ErrGuestExited) {
			return err
		}
		select {
		case <-probeCtx.Done():
			return probeCtx.Err()
		case <-t.C:
		}
	}
}

// admitAnywhere places a session on an existing host of its class, else on
// a new one. Any refusal falls through, since a fresh host can serve it.
// Each host is named on the record before admission, so no guest exists
// without a record naming its host.
func admitAnywhere(ctx context.Context, deps *ScratchCreateDeps, scratch *schema.Scratch, class ClassConfig) (Instance, error) {
	labels := hostLabels(scratch.MachineClass, class.InstanceTemplate)
	now := time.Now().UTC()
	for _, host := range listHosts(ctx, deps.GCE, deps.Zones, labels) {
		if !admissible(host, now, hostMaxAge) {
			continue
		}
		if err := place(ctx, deps.Scratches, scratch, host); err != nil {
			return Instance{}, err
		}
		err := deps.Hosts.Admit(ctx, host, scratch.ID)
		if err == nil {
			return host, nil
		}
		log.Printf("scratch %s: admit on %s: %v", scratch.ID, host.Name, err)
	}
	// Cold path: create and boot a host.
	name := fmt.Sprintf("scratch-host-%s", uuid.New().String()[:8])
	host, zone, err := insertWithFallthrough(ctx, deps.GCE, deps.Zones, deps.Cooldown, name, class.InstanceTemplate, labels)
	if err != nil {
		if zone != "" {
			// Proactively delete a host that exists but is unusable.
			if derr := deps.GCE.DeleteInstance(context.Background(), zone, name); derr != nil {
				log.Printf("deleting failed host %s/%s: %v", zone, name, derr)
			}
		}
		return Instance{}, errors.Wrap(err, "creating host")
	}
	if err := pollHealthy(ctx, deps.hostHealthTimeout(), deps.healthInterval(), func(ctx context.Context) error {
		return deps.Hosts.Healthz(ctx, host)
	}); err != nil {
		if derr := deps.GCE.DeleteInstance(context.Background(), zone, name); derr != nil {
			log.Printf("deleting unhealthy host %s/%s: %v", zone, name, derr)
		}
		return Instance{}, errors.Wrapf(err, "host %s never became healthy", name)
	}
	if err := place(ctx, deps.Scratches, scratch, host); err != nil {
		return Instance{}, err
	}
	if err := deps.Hosts.Admit(ctx, host, scratch.ID); err != nil {
		return Instance{}, errors.Wrapf(err, "admit on fresh host %s", name)
	}
	return host, nil
}

// place names host on the record and persists it, with its address, since
// the host serves the session's exec API. LastUsed is seeded so the idle
// listing never reads a new record's zero LastUsed as ancient.
func place(ctx context.Context, scratches db.Scratch, scratch *schema.Scratch, host Instance) error {
	scratch.Host, scratch.Zone, scratch.InternalIP = host.Name, host.Zone, host.InternalIP
	scratch.Updated = time.Now().UTC()
	scratch.LastUsed = scratch.Updated
	return errors.Wrap(scratches.Update(ctx, *scratch), "scratches update with host")
}

// releaseScratch destroys the guest a record names. No-op without a Host.
func releaseScratch(ctx context.Context, hosts *HostClient, s schema.Scratch) error {
	if s.Host == "" {
		return nil
	}
	if hosts == nil {
		return errors.Errorf("scratch %s on host %s: no host client configured", s.ID, s.Host)
	}
	return hosts.Delete(ctx, hostOf(s), s.ID)
}

// hostOf is the host a record names, as the host client addresses it.
func hostOf(s schema.Scratch) Instance {
	return Instance{Name: s.Host, Zone: s.Zone, InternalIP: s.InternalIP}
}

func mintID(gen func() string) string {
	if gen != nil {
		return gen()
	}
	return uuid.New().String()
}
