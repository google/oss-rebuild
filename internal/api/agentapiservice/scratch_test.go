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
	"testing"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func newCreateDeps(gce GCE, scratches db.Scratch, idGen func() string) *ScratchCreateDeps {
	return &ScratchCreateDeps{
		Scratches: scratches,
		GCE:       gce,
		Standard:  testStandard(),
		Jumbo:     testJumbo(),
		Zone:      "us-central1-a",
		IDGen:     idGen,
	}
}

func TestScratchCreate_HappyPath(t *testing.T) {
	gce := NewMemoryGCE()
	gce.SetNextIP("10.0.0.42")
	scratches := db.NewMemoryScratch()
	deps := newCreateDeps(gce, scratches, func() string { return "sA" })

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
	deps := newCreateDeps(gce, db.NewMemoryScratch(), func() string { return "sJ" })

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
	deps := newCreateDeps(NewMemoryGCE(), db.NewMemoryScratch(), nil)
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
	deps := newCreateDeps(gce, scratches, func() string { return "sI" })

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
	// failOnce[op]=err returns err once when op is invoked, then clears.
	// op keys mirror the log prefixes: "InsertInstanceFromTemplate", "DeleteInstance", ...
	failOnce map[string]error
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
		failOnce:  map[string]error{},
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

// FailNext registers an error to be returned the next time op runs.
func (m *MemoryGCE) FailNext(op string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failOnce[op] = err
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
	if err, ok := m.failOnce[op]; ok {
		delete(m.failOnce, op)
		return err
	}
	return nil
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
