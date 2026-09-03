// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stockoutErr returns a googleapi.Error shaped like a real GCE
// stockout response so isZoneExhausted classifies it.
func stockoutErr() error {
	return &googleapi.Error{
		Code: 503,
		Errors: []googleapi.ErrorItem{
			{Reason: "ZONE_RESOURCE_POOL_EXHAUSTED", Message: "resource pool exhausted"},
		},
	}
}

func testStandard() ClassConfig {
	return ClassConfig{
		InstanceTemplate: "projects/p/global/instanceTemplates/scratch-host-standard",
	}
}

func testJumbo() *ClassConfig {
	return &ClassConfig{
		InstanceTemplate: "projects/p/global/instanceTemplates/scratch-host-jumbo",
	}
}

func newCreateDeps(gce GCE, hosts *fakeHosts, scratches db.Scratch, idGen func() string) *ScratchCreateDeps {
	return &ScratchCreateDeps{
		Scratches:         scratches,
		GCE:               gce,
		Standard:          testStandard(),
		Jumbo:             testJumbo(),
		Hosts:             hosts.client(),
		Zones:             []string{"us-central1-a"},
		Cooldown:          NewZoneCooldown(time.Minute),
		HealthTimeout:     500 * time.Millisecond,
		HostHealthTimeout: 500 * time.Millisecond,
		HealthInterval:    10 * time.Millisecond,
		IDGen:             idGen,
	}
}

// seedHost registers a running, admissible host in both fakes.
func seedHost(gce *MemoryGCE, hosts *fakeHosts, zone, name, ip string, capacity int) {
	gce.SeedRunning(zone, name, hostLabels(schema.MachineClassStandard), ip, time.Now().UTC())
	hosts.AddHost(name, capacity)
}

func TestScratchCreate_HappyPath(t *testing.T) {
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	seedHost(gce, hosts, "us-central1-a", "h1", "10.0.0.42", 6)
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, hosts, scratches, func() string { return "sA" })

	got, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "build-1", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if got.State != schema.ScratchReady {
		t.Errorf("State = %q; want ready", got.State)
	}
	if got.Host != "h1" || got.Zone != "us-central1-a" {
		t.Errorf("record = %+v; want it to name host h1", got)
	}
	// The record carries the host's address. The guest's never leaves the host.
	if got.InternalIP != "10.0.0.42" {
		t.Errorf("InternalIP = %q; want the host's address", got.InternalIP)
	}
	if got.VMName != "" {
		t.Errorf("VMName = %q; want empty (no per-session VM exists)", got.VMName)
	}
	if got.MachineClass != schema.MachineClassStandard || got.BuildID != "build-1" {
		t.Errorf("class/build = (%q, %q)", got.MachineClass, got.BuildID)
	}
	// No VM was created for the session.
	for _, entry := range gce.Log() {
		if strings.HasPrefix(entry, "InsertInstanceFromTemplate") {
			t.Errorf("log has %q; want no instance created for a session", entry)
		}
	}
	if names := hosts.GuestNames("h1"); len(names) != 1 || names[0] != "sA" {
		t.Errorf("guests = %v; want the session admitted on h1", names)
	}
	rec, _ := scratches.Get(context.Background(), "sA")
	if rec.State != schema.ScratchReady {
		t.Errorf("stored State = %q; want ready", rec.State)
	}
}

func TestScratchCreate_JumboUsesJumboHostTemplate(t *testing.T) {
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	// A standard host exists but must not take a jumbo session.
	seedHost(gce, hosts, "us-central1-a", "h-std", "10.0.0.1", 6)
	hosts.AutoProvision(1)
	deps := newCreateDeps(gce, hosts, db.NewMemoryScratch(), func() string { return "sJ" })

	got, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassJumbo,
	}, deps)
	if err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if got.Host == "h-std" {
		t.Errorf("Host = %q; want a jumbo host, not the standard one", got.Host)
	}
	var insert string
	for _, entry := range gce.Log() {
		if strings.HasPrefix(entry, "InsertInstanceFromTemplate") {
			insert = entry
		}
	}
	if !strings.Contains(insert, "scratch-host-jumbo") {
		t.Errorf("insert log = %q; want the jumbo host template", insert)
	}
	inst, err := gce.GetInstance(context.Background(), got.Zone, got.Host)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if inst.Labels[hostLabelClassKey] != "jumbo" {
		t.Errorf("host labels = %v; want scratch_class=jumbo", inst.Labels)
	}
}

func TestScratchCreate_UnknownMachineClass(t *testing.T) {
	deps := newCreateDeps(NewMemoryGCE(), newFakeHosts(), db.NewMemoryScratch(), nil)
	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: "exotic",
	}, deps)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %s; want InvalidArgument. err=%v", status.Code(err), err)
	}
}

func TestScratchCreate_JumboNotConfigured(t *testing.T) {
	deps := newCreateDeps(NewMemoryGCE(), newFakeHosts(), db.NewMemoryScratch(), nil)
	deps.Jumbo = nil
	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassJumbo,
	}, deps)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %s; want InvalidArgument. err=%v", status.Code(err), err)
	}
}

func TestScratchCreate_ColdHostInsertFails(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", errors.New("region full"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, newFakeHosts(), scratches, func() string { return "sI" })

	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	rec, err := scratches.Get(context.Background(), "sI")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_GuestHealthzPollsUntilSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gce := NewMemoryGCE()
		hosts := newFakeHosts()
		seedHost(gce, hosts, "us-central1-a", "h1", "10.0.0.1", 6)
		hosts.QueueGuestHealthz("h1", errors.New("not yet"), errors.New("not yet"))
		deps := newCreateDeps(gce, hosts, db.NewMemoryScratch(), func() string { return "sP" })

		if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
			BuildID: "b", MachineClass: schema.MachineClassStandard,
		}, deps); err != nil {
			t.Fatalf("ScratchCreate: %v", err)
		}
		probes := countLog(hosts.Log(), "GuestHealthz(")
		if probes != 3 {
			t.Errorf("guest probes = %d; want 3 (two failures then success)", probes)
		}
	})
}

func TestScratchCreate_GuestHealthzTimeoutTriggersTeardown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gce := NewMemoryGCE()
		hosts := newFakeHosts()
		seedHost(gce, hosts, "us-central1-a", "h1", "10.0.0.1", 6)
		hosts.FailGuestHealthz("h1", errors.New("nope"))
		scratches := db.NewMemoryScratch()
		deps := newCreateDeps(gce, hosts, scratches, func() string { return "sT" })

		_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
			BuildID: "b", MachineClass: schema.MachineClassStandard,
		}, deps)
		if status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("code = %s; want DeadlineExceeded. err=%v", status.Code(err), err)
		}
		probes := countLog(hosts.Log(), "GuestHealthz(")
		if probes < 2 {
			t.Errorf("guest probes = %d; want >= 2 (polling loop)", probes)
		}
		// The guest is destroyed and the host survives for the next session.
		if names := hosts.GuestNames("h1"); len(names) != 0 {
			t.Errorf("guests = %v; want the session's guest torn down", names)
		}
		if !gce.InstanceExists("us-central1-a", "h1") {
			t.Error("host deleted with its guest; want it left for the next session")
		}
		rec, err := scratches.Get(context.Background(), "sT")
		if err != nil {
			t.Fatalf("Get after teardown: %v", err)
		}
		if rec.State != schema.ScratchDeleted {
			t.Errorf("State = %q; want deleted", rec.State)
		}
	})
}

func TestScratchCreate_FallsThroughOnStockout(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	hosts := newFakeHosts()
	hosts.AutoProvision(6)
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, hosts, scratches, func() string { return "sF" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	got, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if got.Zone != "us-central1-b" {
		t.Errorf("Zone = %q; want us-central1-b (fell through after stockout)", got.Zone)
	}
	// Two Insert attempts logged: one per zone.
	var inserts int
	for _, line := range gce.Log() {
		if strings.HasPrefix(line, "InsertInstanceFromTemplate(") {
			inserts++
		}
	}
	if inserts != 2 {
		t.Errorf("InsertInstanceFromTemplate calls = %d; want 2", inserts)
	}
	if !gce.InstanceExists("us-central1-b", got.Host) {
		t.Errorf("host not created in winning zone")
	}
	if gce.InstanceExists("us-central1-a", got.Host) {
		t.Errorf("host erroneously exists in stockout zone")
	}
}

func TestScratchCreate_AllZonesExhausted(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, newFakeHosts(), scratches, func() string { return "sX" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	if !strings.Contains(err.Error(), "all zones exhausted") {
		t.Errorf("error = %q; want it to mention all zones exhausted", err.Error())
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("code = %v; want ResourceExhausted", status.Code(err))
	}
	rec, _ := scratches.Get(context.Background(), "sX")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_NonStockoutDoesNotFallThrough(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", errors.New("permission denied: invalid template"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, newFakeHosts(), scratches, func() string { return "sN" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	var inserts int
	for _, line := range gce.Log() {
		if strings.HasPrefix(line, "InsertInstanceFromTemplate(") {
			inserts++
		}
	}
	if inserts != 1 {
		t.Errorf("InsertInstanceFromTemplate calls = %d; want 1 (non-stockout must not fall through)", inserts)
	}
}

// countLog counts the log entries with prefix.
func countLog(log []string, prefix string) int {
	var n int
	for _, line := range log {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func deleteZonesFromLog(log []string) []string {
	var out []string
	for _, line := range log {
		if !strings.HasPrefix(line, "DeleteInstance(") {
			continue
		}
		z := strings.TrimPrefix(line, "DeleteInstance(")
		if i := strings.Index(z, ","); i >= 0 {
			z = z[:i]
		}
		out = append(out, z)
	}
	return out
}

func TestScratchCreate_NonStockoutCleansUpPossibleOrphan(t *testing.T) {
	// zone[0] stockout (safe, no host), then fall through to zone[1] where
	// Insert returns a non-stockout error (e.g. ctx cancel after the op
	// started server-side). Cleanup must target zone[1].
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", errors.New("context canceled mid-op"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, newFakeHosts(), scratches, func() string { return "sO" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	if diff := cmp.Diff([]string{"us-central1-b"}, deleteZonesFromLog(gce.Log())); diff != "" {
		t.Errorf("DeleteInstance target zones mismatch (-want +got):\n%s", diff)
	}
}

func TestScratchCreate_AllStockoutsSkipsDeleteInstance(t *testing.T) {
	// Stockouts are rejected pre-allocation, so no host exists in any
	// zone. Cleanup must NOT issue a DeleteInstance, to avoid needless
	// 404 round-trips and log noise during sustained outages.
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, newFakeHosts(), scratches, func() string { return "sA" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	if got := deleteZonesFromLog(gce.Log()); len(got) != 0 {
		t.Errorf("DeleteInstance called for zones %v; want none (stockouts create no hosts)", got)
	}
	rec, _ := scratches.Get(context.Background(), "sA")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_CooldownSkipsZoneOnSubsequentCall(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr()) // first call: zone[0] stockout
	hosts := newFakeHosts()
	// One-slot hosts, so the second create needs a second host.
	hosts.AutoProvision(1)
	scratches := db.NewMemoryScratch()
	var seq int
	deps := newCreateDeps(gce, hosts, scratches, func() string {
		seq++
		return fmt.Sprintf("sC%d", seq)
	})
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	// First call: zone[0] stockouts, falls through to zone[1].
	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err != nil {
		t.Fatalf("first ScratchCreate: %v", err)
	}

	// Second call: zone[0] should be skipped (cooldown), zone[1] tried first.
	startLog := len(gce.Log())
	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err != nil {
		t.Fatalf("second ScratchCreate: %v", err)
	}
	var attempted []string
	for _, line := range gce.Log()[startLog:] {
		if strings.HasPrefix(line, "InsertInstanceFromTemplate(") {
			// log format: "InsertInstanceFromTemplate(zone,name,template)"
			zone := strings.TrimPrefix(line, "InsertInstanceFromTemplate(")
			if i := strings.Index(zone, ","); i >= 0 {
				zone = zone[:i]
			}
			attempted = append(attempted, zone)
		}
	}
	if len(attempted) != 1 {
		t.Fatalf("second-call Insert attempts = %v; want exactly 1 (zone[0] cooled down)", attempted)
	}
	if attempted[0] != "us-central1-b" {
		t.Errorf("attempted = %q; want us-central1-b (zone[0] should be cooled down)", attempted[0])
	}
}

func TestZoneCooldown_ActivePreservesOrderAndExpires(t *testing.T) {
	zones := []string{"a", "b", "c"}

	// Live cooldown: mark sticks, order is preserved.
	c := NewZoneCooldown(time.Hour)
	if diff := cmp.Diff(zones, c.active(zones)); diff != "" {
		t.Errorf("empty cooldown active mismatch (-want +got):\n%s", diff)
	}
	c.mark("b")
	if diff := cmp.Diff([]string{"a", "c"}, c.active(zones)); diff != "" {
		t.Errorf("after mark(b) active mismatch (-want +got):\n%s", diff)
	}

	// Instantly-expiring cooldown: mark is observable but pruned on next read.
	expired := NewZoneCooldown(time.Nanosecond)
	expired.mark("a")
	time.Sleep(time.Millisecond)
	if diff := cmp.Diff(zones, expired.active(zones)); diff != "" {
		t.Errorf("after expiry active mismatch (-want +got):\n%s", diff)
	}
}

func TestScratchGet_HappyAndMissing(t *testing.T) {
	scratches := db.NewMemoryScratch()
	want := schema.Scratch{ID: "s1", State: schema.ScratchReady, InternalIP: "10.0.0.7"}
	if err := scratches.Insert(context.Background(), want); err != nil {
		t.Fatalf("seed: %v", err)
	}
	deps := &ScratchGetDeps{Scratches: scratches}

	got, err := ScratchGet(context.Background(), schema.ScratchGetRequest{ScratchID: "s1"}, deps)
	if err != nil {
		t.Fatalf("ScratchGet: %v", err)
	}
	if got.ID != "s1" || got.InternalIP != "10.0.0.7" {
		t.Errorf("got = %+v; want s1 / 10.0.0.7", got)
	}

	_, err = ScratchGet(context.Background(), schema.ScratchGetRequest{ScratchID: "missing"}, deps)
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %s; want NotFound. err=%v", status.Code(err), err)
	}
}

func TestScratchDelete_TearsDownTheGuest(t *testing.T) {
	gce := NewMemoryGCE()
	hosts := newFakeHosts()
	seedHost(gce, hosts, "us-central1-a", "h1", "10.0.0.1", 6)
	scratches := db.NewMemoryScratch()

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, newCreateDeps(gce, hosts, scratches, func() string { return "sD" })); err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	resp, err := ScratchDelete(context.Background(), schema.ScratchDeleteRequest{ScratchID: "sD"},
		&ScratchDeleteDeps{Scratches: scratches, Hosts: hosts.client()})
	if err != nil {
		t.Fatalf("ScratchDelete: %v", err)
	}
	if resp.ScratchID != "sD" || resp.State != schema.ScratchDeleted {
		t.Errorf("resp = %+v; want sD/deleted", resp)
	}
	if got := hosts.GuestNames("h1"); len(got) != 0 {
		t.Errorf("guests = %v; want the session's guest destroyed", got)
	}
	// The host itself survives its session's teardown.
	if !gce.InstanceExists("us-central1-a", "h1") {
		t.Error("host deleted with its guest; want it left for the next session")
	}
	rec, _ := scratches.Get(context.Background(), "sD")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchDelete_GCEFailureStaysDeleting(t *testing.T) {
	gce := NewMemoryGCE()
	scratches := db.NewMemoryScratch()
	zone := "us-central1-a"
	_, _ = gce.InsertInstanceFromTemplate(context.Background(), zone, "scratch-s1", "tmpl", nil)
	if err := scratches.Insert(context.Background(), schema.Scratch{
		ID: "s1", State: schema.ScratchReady, Zone: zone, VMName: "scratch-s1",
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}
	gce.FailNext("DeleteInstance", errors.New("gce unavailable"))

	_, err := ScratchDelete(context.Background(), schema.ScratchDeleteRequest{ScratchID: "s1"},
		&ScratchDeleteDeps{Scratches: scratches, GCE: gce})
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %s; want Internal. err=%v", status.Code(err), err)
	}
	rec, _ := scratches.Get(context.Background(), "s1")
	if rec.State != schema.ScratchDeleting {
		t.Errorf("State = %q; want deleting (held for the reaper)", rec.State)
	}
	if !gce.InstanceExists(zone, "scratch-s1") {
		t.Errorf("instance gone despite failed delete")
	}
}

func TestScratchDelete_NotFound(t *testing.T) {
	_, err := ScratchDelete(context.Background(), schema.ScratchDeleteRequest{ScratchID: "missing"},
		&ScratchDeleteDeps{Scratches: db.NewMemoryScratch(), Hosts: newFakeHosts().client()})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %s; want NotFound. err=%v", status.Code(err), err)
	}
}

// MemoryGCE is an in-memory fake of GCE for tests. It records the operation
// sequence and supports targeted failure injection.
type MemoryGCE struct {
	mu        sync.Mutex
	log       []string // human-readable: "InsertInstanceFromTemplate(zone,name,...)" etc.
	instances map[string]Instance
	labels    map[string]map[string]string // instance key -> labels
	// failQueue[op] is a FIFO of errors to return on successive invocations
	// of op (one per call). op keys mirror the log prefixes:
	// "InsertInstanceFromTemplate", "DeleteInstance", ...
	failQueue map[string][]error
	// nextIP supplies the InternalIP for the next InsertInstance call. If
	// empty, a deterministic "10.0.0.N" address is assigned.
	nextIP string
	ipSeq  int
}

// NewMemoryGCE returns an empty fake.
func NewMemoryGCE() *MemoryGCE {
	return &MemoryGCE{
		instances: map[string]Instance{},
		labels:    map[string]map[string]string{},
		failQueue: map[string][]error{},
	}
}

// SeedRunning creates a running instance without going through Insert.
func (m *MemoryGCE) SeedRunning(zone, name string, labels map[string]string, internalIP string, created time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := zone + "/" + name
	m.instances[key] = Instance{
		Name: name, Zone: zone, InternalIP: internalIP,
		Labels: copyLabels(labels), LabelFingerprint: "fp0", Created: created,
	}
	m.labels[key] = copyLabels(labels)
}

func copyLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// FailNext queues an error to be returned the next time op runs.
// Successive FailNext calls queue errors in FIFO order.
func (m *MemoryGCE) FailNext(op string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failQueue[op] = append(m.failQueue[op], err)
}

// SetNextIP fixes the InternalIP for the next InsertInstanceFromTemplate.
func (m *MemoryGCE) SetNextIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextIP = ip
}

// Log returns the sequence of operations performed so far.
func (m *MemoryGCE) Log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.log))
	copy(out, m.log)
	return out
}

// InstanceExists reports whether name exists in zone.
func (m *MemoryGCE) InstanceExists(zone, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.instances[zone+"/"+name]
	return ok
}

func (m *MemoryGCE) checkFail(op string) error {
	q := m.failQueue[op]
	if len(q) == 0 {
		return nil
	}
	err := q[0]
	if len(q) == 1 {
		delete(m.failQueue, op)
	} else {
		m.failQueue[op] = q[1:]
	}
	return err
}

func (m *MemoryGCE) record(format string, args ...any) {
	m.log = append(m.log, fmt.Sprintf(format, args...))
}

func (m *MemoryGCE) InsertInstanceFromTemplate(_ context.Context, zone, name, templateURL string, labels map[string]string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("InsertInstanceFromTemplate(%s,%s,%s)", zone, name, templateURL)
	if err := m.checkFail("InsertInstanceFromTemplate"); err != nil {
		return Instance{}, err
	}
	key := zone + "/" + name
	if _, exists := m.instances[key]; exists {
		return Instance{}, errors.New("instance already exists")
	}
	ip := m.nextIP
	if ip == "" {
		m.ipSeq++
		ip = fmt.Sprintf("10.0.0.%d", m.ipSeq)
	}
	m.nextIP = ""
	inst := Instance{
		Name: name, Zone: zone, InternalIP: ip,
		Labels: copyLabels(labels), LabelFingerprint: "fp0", Created: time.Now().UTC(),
	}
	m.instances[key] = inst
	m.labels[key] = copyLabels(labels)
	// Newly-inserted instances are running.
	return inst, nil
}

func (m *MemoryGCE) GetInstance(_ context.Context, zone, name string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("GetInstance(%s,%s)", zone, name)
	if err := m.checkFail("GetInstance"); err != nil {
		return Instance{}, err
	}
	inst, ok := m.instances[zone+"/"+name]
	if !ok {
		return Instance{}, errors.New("instance not found")
	}
	return inst, nil
}

func (m *MemoryGCE) ListRunningInstances(_ context.Context, zone string, labels map[string]string) ([]Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("ListRunningInstances(%s,%v)", zone, labels)
	if err := m.checkFail("ListRunningInstances"); err != nil {
		return nil, err
	}
	var out []Instance
	for key, inst := range m.instances {
		if inst.Zone != zone || !labelsMatch(m.labels[key], labels) {
			continue
		}
		out = append(out, inst)
	}
	// Name order, so a fan-out over candidates is deterministic.
	slices.SortFunc(out, func(a, b Instance) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *MemoryGCE) SetLabels(_ context.Context, zone, name string, labels map[string]string, fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("SetLabels(%s,%s,%v)", zone, name, labels)
	if err := m.checkFail("SetLabels"); err != nil {
		return err
	}
	key := zone + "/" + name
	inst, ok := m.instances[key]
	if !ok {
		return errors.New("instance not found")
	}
	if inst.LabelFingerprint != fingerprint {
		return errors.New("labelFingerprint mismatch")
	}
	m.labels[key] = copyLabels(labels)
	inst.Labels = copyLabels(labels)
	inst.LabelFingerprint = fingerprint + "+"
	m.instances[key] = inst
	return nil
}

func (m *MemoryGCE) DeleteInstance(_ context.Context, zone, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("DeleteInstance(%s,%s)", zone, name)
	if err := m.checkFail("DeleteInstance"); err != nil {
		return err
	}
	delete(m.instances, zone+"/"+name) // missing is success, as in the real client
	return nil
}
