// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
		InstanceTemplate: "projects/p/global/instanceTemplates/builder-standard",
	}
}

func testJumbo() *ClassConfig {
	return &ClassConfig{
		InstanceTemplate: "projects/p/global/instanceTemplates/builder-jumbo",
	}
}

func newCreateDeps(gce GCE, scratches db.Scratch, probe HealthProbe, idGen func() string) *ScratchCreateDeps {
	return &ScratchCreateDeps{
		Scratches:      scratches,
		GCE:            gce,
		Standard:       testStandard(),
		Jumbo:          testJumbo(),
		Zones:          []string{"us-central1-a"},
		Cooldown:       NewZoneCooldown(time.Minute),
		HealthProbe:    probe,
		HealthTimeout:  500 * time.Millisecond,
		HealthInterval: 10 * time.Millisecond,
		IDGen:          idGen,
	}
}

// okProbe is a HealthProbe that always succeeds, for tests that don't
// care about the probe behavior.
func okProbe(_ context.Context, _ string) error { return nil }

// healthyAfter returns a HealthProbe that fails the first n-1 calls and
// succeeds afterwards. counter is updated atomically with each invocation.
func healthyAfter(counter *int32, n int32) HealthProbe {
	return func(_ context.Context, _ string) error {
		c := atomic.AddInt32(counter, 1)
		if c < n {
			return errors.New("not yet")
		}
		return nil
	}
}

func alwaysUnhealthy(counter *int32) HealthProbe {
	return func(_ context.Context, _ string) error {
		atomic.AddInt32(counter, 1)
		return errors.New("nope")
	}
}

func TestScratchCreate_HappyPath(t *testing.T) {
	gce := NewMemoryGCE()
	gce.SetNextIP("10.0.0.42")
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sA" })

	got, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "build-1", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if got.State != schema.ScratchReady {
		t.Errorf("State = %q; want ready", got.State)
	}
	if got.InternalIP != "10.0.0.42" {
		t.Errorf("InternalIP = %q; want 10.0.0.42", got.InternalIP)
	}
	if got.VMName != "scratch-sA" {
		t.Errorf("VMName = %q; want scratch-sA", got.VMName)
	}
	if got.MachineClass != schema.MachineClassStandard || got.BuildID != "build-1" {
		t.Errorf("class/build = (%q, %q)", got.MachineClass, got.BuildID)
	}

	gotLog := gce.Log()
	if len(gotLog) < 1 || !strings.HasPrefix(gotLog[0], "InsertInstanceFromTemplate(") {
		t.Errorf("log[0] = %q; want InsertInstanceFromTemplate(...", gotLog)
	}

	rec, _ := scratches.Get(context.Background(), "sA")
	if rec.State != schema.ScratchReady {
		t.Errorf("stored State = %q; want ready", rec.State)
	}
}

func TestScratchCreate_JumboUsesJumboTemplate(t *testing.T) {
	gce := NewMemoryGCE()
	deps := newCreateDeps(gce, db.NewMemoryScratch(), okProbe, func() string { return "sJ" })

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassJumbo,
	}, deps); err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if !strings.Contains(gce.Log()[0], "builder-jumbo") {
		t.Errorf("InsertInstanceFromTemplate log = %q; want builder-jumbo template", gce.Log()[0])
	}
}

func TestScratchCreate_UnknownMachineClass(t *testing.T) {
	deps := newCreateDeps(NewMemoryGCE(), db.NewMemoryScratch(), okProbe, nil)
	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: "exotic",
	}, deps)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %s; want InvalidArgument. err=%v", status.Code(err), err)
	}
}

func TestScratchCreate_InstanceInsertFails(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", errors.New("region full"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sI" })

	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	if gce.InstanceExists("us-central1-a", "scratch-sI") {
		t.Errorf("instance still exists after teardown")
	}
	rec, err := scratches.Get(context.Background(), "sI")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_HealthzPollsUntilSuccess(t *testing.T) {
	gce := NewMemoryGCE()
	var probeCount int32
	const wantCalls = int32(3)
	deps := newCreateDeps(gce, db.NewMemoryScratch(), healthyAfter(&probeCount, wantCalls), func() string { return "sP" })

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err != nil {
		t.Fatalf("ScratchCreate: %v", err)
	}
	if got := atomic.LoadInt32(&probeCount); got != wantCalls {
		t.Errorf("probe calls = %d; want %d", got, wantCalls)
	}
}

func TestScratchCreate_HealthzTimeoutTriggersTeardown(t *testing.T) {
	gce := NewMemoryGCE()
	scratches := db.NewMemoryScratch()
	var probeCount int32
	deps := newCreateDeps(gce, scratches, alwaysUnhealthy(&probeCount), func() string { return "sT" })

	_, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("code = %s; want DeadlineExceeded. err=%v", status.Code(err), err)
	}
	if atomic.LoadInt32(&probeCount) < 2 {
		t.Errorf("probe calls = %d; want >= 2 (polling loop)", probeCount)
	}
	if gce.InstanceExists("us-central1-a", "scratch-sT") {
		t.Errorf("instance still exists after teardown")
	}
	rec, err := scratches.Get(context.Background(), "sT")
	if err != nil {
		t.Fatalf("Get after teardown: %v", err)
	}
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_FallsThroughOnStockout(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sF" })
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
	if !gce.InstanceExists("us-central1-b", "scratch-sF") {
		t.Errorf("VM not created in winning zone")
	}
	if gce.InstanceExists("us-central1-a", "scratch-sF") {
		t.Errorf("VM erroneously exists in stockout zone")
	}
}

func TestScratchCreate_AllZonesExhausted(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sX" })
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
	rec, _ := scratches.Get(context.Background(), "sX")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_NonStockoutDoesNotFallThrough(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", errors.New("permission denied: invalid template"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sN" })
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

// deleteZonesFromLog returns the zone arg of every recorded
// DeleteInstance call.
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
	// zone[0] stockout (safe, no VM); fall through to zone[1] where
	// Insert returns a non-stockout error (e.g. ctx cancel after the op
	// started server-side). Cleanup must target zone[1].
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", errors.New("context canceled mid-op"))
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sO" })
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
	// Stockouts are rejected pre-allocation, so no VM exists in any
	// zone. Cleanup must NOT issue a DeleteInstance, to avoid needless
	// 404 round-trips and log noise during sustained outages.
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr())
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, okProbe, func() string { return "sA" })
	deps.Zones = []string{"us-central1-a", "us-central1-b"}

	if _, err := ScratchCreate(context.Background(), schema.ScratchCreateRequest{
		BuildID: "b", MachineClass: schema.MachineClassStandard,
	}, deps); err == nil {
		t.Fatalf("ScratchCreate succeeded; want failure")
	}
	if got := deleteZonesFromLog(gce.Log()); len(got) != 0 {
		t.Errorf("DeleteInstance called for zones %v; want none (stockouts create no VMs)", got)
	}
	rec, _ := scratches.Get(context.Background(), "sA")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchCreate_CooldownSkipsZoneOnSubsequentCall(t *testing.T) {
	gce := NewMemoryGCE()
	gce.FailNext("InsertInstanceFromTemplate", stockoutErr()) // first call: zone[0] stockout
	scratches := db.NewMemoryScratch()
	var seq int
	deps := newCreateDeps(gce, scratches, okProbe, func() string {
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

func TestScratchDelete_HappyPath(t *testing.T) {
	gce := NewMemoryGCE()
	scratches := db.NewMemoryScratch()
	zone := "us-central1-a"
	_, _ = gce.InsertInstanceFromTemplate(context.Background(), zone, "scratch-s1", "tmpl", nil)
	if err := scratches.Insert(context.Background(), schema.Scratch{
		ID: "s1", State: schema.ScratchReady, Zone: zone, VMName: "scratch-s1",
	}); err != nil {
		t.Fatalf("seed scratch: %v", err)
	}

	resp, err := ScratchDelete(context.Background(), schema.ScratchDeleteRequest{ScratchID: "s1"},
		&ScratchDeleteDeps{Scratches: scratches, GCE: gce})
	if err != nil {
		t.Fatalf("ScratchDelete: %v", err)
	}
	if resp.ScratchID != "s1" || resp.State != schema.ScratchDeleted {
		t.Errorf("resp = %+v; want s1/deleted", resp)
	}
	if gce.InstanceExists(zone, "scratch-s1") {
		t.Errorf("instance still exists after delete")
	}
	rec, _ := scratches.Get(context.Background(), "s1")
	if rec.State != schema.ScratchDeleted {
		t.Errorf("State = %q; want deleted", rec.State)
	}
}

func TestScratchDelete_NotFound(t *testing.T) {
	_, err := ScratchDelete(context.Background(), schema.ScratchDeleteRequest{ScratchID: "missing"},
		&ScratchDeleteDeps{Scratches: db.NewMemoryScratch(), GCE: NewMemoryGCE()})
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
	stopped   map[string]bool              // instance key -> stopped (true means terminated, missing/false means running)
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
		stopped:   map[string]bool{},
		failQueue: map[string][]error{},
	}
}

// SeedStopped pre-creates a stopped instance with the given labels. Used by
// tests to populate the warm pool without going through Insert+Stop.
func (m *MemoryGCE) SeedStopped(zone, name string, labels map[string]string, internalIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := zone + "/" + name
	m.instances[key] = Instance{Name: name, Zone: zone, InternalIP: internalIP}
	m.labels[key] = copyLabels(labels)
	m.stopped[key] = true
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
	inst := Instance{Name: name, Zone: zone, InternalIP: ip}
	m.instances[key] = inst
	m.labels[key] = copyLabels(labels)
	// Newly-inserted instances are running.
	return inst, nil
}

func (m *MemoryGCE) ListStoppedInstances(_ context.Context, zone string, labels map[string]string) ([]Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("ListStoppedInstances(%s,%v)", zone, labels)
	if err := m.checkFail("ListStoppedInstances"); err != nil {
		return nil, err
	}
	var out []Instance
	for key, inst := range m.instances {
		if inst.Zone != zone {
			continue
		}
		if !m.stopped[key] {
			continue
		}
		if !labelsMatch(m.labels[key], labels) {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

func (m *MemoryGCE) StartInstance(_ context.Context, zone, name string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("StartInstance(%s,%s)", zone, name)
	if err := m.checkFail("StartInstance"); err != nil {
		return Instance{}, err
	}
	key := zone + "/" + name
	inst, ok := m.instances[key]
	if !ok {
		return Instance{}, errors.New("instance not found")
	}
	delete(m.stopped, key)
	// Real Compute can change the InternalIP on start; mint a fresh one
	// so callers must use the returned value rather than the seeded one.
	m.ipSeq++
	inst.InternalIP = fmt.Sprintf("10.0.0.%d", m.ipSeq)
	m.instances[key] = inst
	return inst, nil
}

func (m *MemoryGCE) StopInstance(_ context.Context, zone, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("StopInstance(%s,%s)", zone, name)
	if err := m.checkFail("StopInstance"); err != nil {
		return err
	}
	key := zone + "/" + name
	if _, ok := m.instances[key]; !ok {
		return errors.New("instance not found")
	}
	m.stopped[key] = true
	return nil
}

func (m *MemoryGCE) DeleteInstance(_ context.Context, zone, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record("DeleteInstance(%s,%s)", zone, name)
	if err := m.checkFail("DeleteInstance"); err != nil {
		return err
	}
	key := zone + "/" + name
	if _, ok := m.instances[key]; !ok {
		return errors.New("instance not found")
	}
	delete(m.instances, key)
	return nil
}
