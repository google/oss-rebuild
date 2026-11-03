// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/api/scratchhostservice"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeHosts fakes a fleet of host brokers keyed by host name.
type fakeHosts struct {
	onGuests func() // runs before each Guests answer
	mu       sync.Mutex
	log      []string
	hosts    map[string]*fakeHost
	autoCap  int              // AutoProvision's capacity
	listE    map[string]error // host name -> error Guests returns
	downE    map[string]error // host name -> error Healthz returns
	guestE   map[string]error // host -> sticky GuestHealthz error
	guestQ   map[string][]error
}

type fakeHost struct {
	capacity   int
	guests     map[string]scratchhostservice.Guest
	lastServed time.Time
}

func newFakeHosts() *fakeHosts {
	return &fakeHosts{
		hosts:  map[string]*fakeHost{},
		listE:  map[string]error{},
		downE:  map[string]error{},
		guestE: map[string]error{},
		guestQ: map[string][]error{},
	}
}

func (m *fakeHosts) AddHost(name string, capacity int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hosts[name] = &fakeHost{capacity: capacity, guests: map[string]scratchhostservice.Guest{}}
}

// AutoProvision makes unknown hosts appear with capacity on first Healthz,
// as a cold host's broker comes up. Zero keeps them unreachable.
func (m *fakeHosts) AutoProvision(capacity int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoCap = capacity
}

// SeedGuest places a guest on a host without going through Admit.
func (m *fakeHosts) SeedGuest(host, scratchID string, since time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hosts[host].guests[scratchID] = scratchhostservice.Guest{ScratchID: scratchID, Since: since}
	m.hosts[host].lastServed = since
}

func (m *fakeHosts) FailGuests(host string, err error)  { m.set(m.listE, host, err) }
func (m *fakeHosts) FailHealthz(host string, err error) { m.set(m.downE, host, err) }

// FailGuestHealthz makes every GuestHealthz on host fail with err.
func (m *fakeHosts) FailGuestHealthz(host string, err error) { m.set(m.guestE, host, err) }

// QueueGuestHealthz queues errors for host's next GuestHealthz calls, FIFO.
func (m *fakeHosts) QueueGuestHealthz(host string, errs ...error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guestQ[host] = append(m.guestQ[host], errs...)
}

func (m *fakeHosts) set(into map[string]error, host string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	into[host] = err
}

func (m *fakeHosts) Log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

// GuestNames returns the scratch ids live on a host.
func (m *fakeHosts) GuestNames(host string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if h, ok := m.hosts[host]; ok {
		for id := range h.guests {
			out = append(out, id)
		}
	}
	return out
}

func (m *fakeHosts) admit(host Instance, scratchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf("Admit(%s,%s)", host.Name, scratchID))
	h, ok := m.hosts[host.Name]
	if !ok {
		return errors.New("no such host")
	}
	if _, live := h.guests[scratchID]; live {
		return nil // idempotent
	}
	if len(h.guests) >= h.capacity {
		return errors.New("host full")
	}
	h.guests[scratchID] = scratchhostservice.Guest{ScratchID: scratchID}
	h.lastServed = time.Now().UTC()
	return nil
}

func (m *fakeHosts) delete(host Instance, scratchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf("Delete(%s,%s)", host.Name, scratchID))
	if h, ok := m.hosts[host.Name]; ok {
		if _, live := h.guests[scratchID]; live {
			delete(h.guests, scratchID)
			h.lastServed = time.Now().UTC()
		}
	}
	return nil
}

func (m *fakeHosts) guests(host Instance) (scratchhostservice.GuestList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf("Guests(%s)", host.Name))
	if err := m.listE[host.Name]; err != nil {
		return scratchhostservice.GuestList{}, err
	}
	h, ok := m.hosts[host.Name]
	if !ok {
		return scratchhostservice.GuestList{}, errors.New("no such host")
	}
	out := scratchhostservice.GuestList{LastServed: h.lastServed}
	for _, g := range h.guests {
		out.Guests = append(out.Guests, g)
	}
	return out, nil
}

func (m *fakeHosts) healthz(host Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf("Healthz(%s)", host.Name))
	if err := m.downE[host.Name]; err != nil {
		return err
	}
	if _, ok := m.hosts[host.Name]; !ok {
		if m.autoCap <= 0 {
			return errors.New("no such host")
		}
		m.hosts[host.Name] = &fakeHost{capacity: m.autoCap, guests: map[string]scratchhostservice.Guest{}}
	}
	return nil
}

func (m *fakeHosts) guestHealthz(host Instance, scratchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf("GuestHealthz(%s,%s)", host.Name, scratchID))
	if q := m.guestQ[host.Name]; len(q) > 0 {
		m.guestQ[host.Name] = q[1:]
		return q[0]
	}
	if err := m.guestE[host.Name]; err != nil {
		return err
	}
	h, ok := m.hosts[host.Name]
	if !ok {
		return errors.New("no such host")
	}
	if _, live := h.guests[scratchID]; !live {
		return errors.New("no such guest")
	}
	return nil
}

// client returns a HostClient whose requests reach these fake brokers by host
// name, in process, so tests run the real client and act's wire format.
func (m *fakeHosts) client() *HostClient {
	if m == nil {
		return nil
	}
	return &HostClient{Dial: func(host Instance) (httpx.BasicClient, *url.URL, error) {
		return brokerClient{m, host}, &url.URL{Scheme: "http", Host: "broker.invalid"}, nil
	}}
}

// brokerClient serves one fake host's broker routes. A status error from the
// fake becomes the broker's response, and any other error fails the request
// as a transport error would.
type brokerClient struct {
	hosts *fakeHosts
	host  Instance
}

func (c brokerClient) Do(req *http.Request) (*http.Response, error) {
	m, host := c.hosts, c.host
	var dropped error
	wire := func(err error) error {
		if _, ok := status.FromError(err); !ok {
			dropped = err
		}
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(scratchhostservice.RouteAdmit, api.Handler(api.NoDepsInit, func(_ context.Context, r scratchhostservice.AdmitRequest, _ *api.NoDeps) (*scratchhostservice.Guest, error) {
		if err := wire(m.admit(host, r.ScratchID)); err != nil {
			return nil, err
		}
		return &scratchhostservice.Guest{ScratchID: r.ScratchID}, nil
	}))
	mux.Handle(scratchhostservice.RouteDelete, api.Handler(api.NoDepsInit, func(_ context.Context, r scratchhostservice.GuestRequest, _ *api.NoDeps) (*act.NoOutput, error) {
		if err := wire(m.delete(host, r.ScratchID)); err != nil {
			return nil, err
		}
		return &act.NoOutput{}, nil
	}))
	mux.Handle(scratchhostservice.RouteList, api.Handler(api.NoDepsInit, func(_ context.Context, _ scratchhostservice.ListRequest, _ *api.NoDeps) (*scratchhostservice.GuestList, error) {
		if m.onGuests != nil {
			m.onGuests()
		}
		list, err := m.guests(host)
		if err := wire(err); err != nil {
			return nil, err
		}
		return &list, nil
	}))
	mux.Handle(scratchhostservice.RouteGuestHealth, api.Handler(api.NoDepsInit, func(_ context.Context, r scratchhostservice.GuestRequest, _ *api.NoDeps) (*act.NoOutput, error) {
		if err := wire(m.guestHealthz(host, r.ScratchID)); err != nil {
			return nil, err
		}
		return &act.NoOutput{}, nil
	}))
	mux.HandleFunc(scratchhostservice.RouteHealthz, func(rw http.ResponseWriter, _ *http.Request) {
		if err := wire(m.healthz(host)); err != nil {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if dropped != nil {
		return nil, dropped
	}
	return rec.Result(), nil
}

// TestHostClient runs the client against handlers built from the
// broker's own types, pinning the status codes it switches on.
func TestHostClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(scratchhostservice.RouteAdmit, api.Handler(api.NoDepsInit, func(_ context.Context, req scratchhostservice.AdmitRequest, _ *api.NoDeps) (*scratchhostservice.Guest, error) {
		if req.ScratchID == "full" {
			return nil, api.AsStatus(codes.ResourceExhausted, errors.New("host full"))
		}
		return &scratchhostservice.Guest{ScratchID: req.ScratchID, IP: "10.200.0.2"}, nil
	}))
	mux.Handle(scratchhostservice.RouteDelete, api.Handler(api.NoDepsInit, func(context.Context, scratchhostservice.GuestRequest, *api.NoDeps) (*act.NoOutput, error) {
		return nil, api.AsStatus(codes.NotFound, errors.New("no such guest"))
	}))
	mux.Handle(scratchhostservice.RouteList, api.Handler(api.NoDepsInit, func(context.Context, scratchhostservice.ListRequest, *api.NoDeps) (*scratchhostservice.GuestList, error) {
		return &scratchhostservice.GuestList{Guests: []scratchhostservice.Guest{{ScratchID: "s1"}}}, nil
	}))
	mux.Handle(scratchhostservice.RouteGuestHealth, api.Handler(api.NoDepsInit, func(context.Context, scratchhostservice.GuestRequest, *api.NoDeps) (*act.NoOutput, error) {
		return &act.NoOutput{}, nil
	}))
	mux.HandleFunc(scratchhostservice.RouteHealthz, func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	var (
		mu       sync.Mutex
		gotPaths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		mux.ServeHTTP(rw, r)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	hosts := &HostClient{Dial: func(Instance) (httpx.BasicClient, *url.URL, error) {
		return http.DefaultClient, base, nil
	}}
	host := Instance{Name: "h1", Zone: "z", InternalIP: "10.0.0.1"}
	ctx := context.Background()
	if err := hosts.Admit(ctx, host, "s1"); err != nil {
		t.Errorf("Admit: %v", err)
	}
	// A refusal is an error the caller reads as "try elsewhere".
	if err := hosts.Admit(ctx, host, "full"); err == nil {
		t.Error("Admit on a full host = nil; want an error")
	}
	// A delete of a guest that is already gone is the caller's success case.
	if err := hosts.Delete(ctx, host, "s1"); err != nil {
		t.Errorf("Delete on a missing guest = %v; want success", err)
	}
	list, err := hosts.Guests(ctx, host)
	if err != nil || len(list.Guests) != 1 {
		t.Errorf("Guests = %+v, %v; want one guest", list, err)
	}
	if err := hosts.Healthz(ctx, host); err != nil {
		t.Errorf("Healthz: %v", err)
	}
	if err := hosts.GuestHealthz(ctx, host, "s1"); err != nil {
		t.Errorf("GuestHealthz: %v", err)
	}
	joined := strings.Join(gotPaths, " ")
	for _, want := range []string{"POST /guest/admit", "POST /guest/delete", "POST /guests", "GET /healthz", "POST /guest/healthz"} {
		if !strings.Contains(joined, want) {
			t.Errorf("paths = %v; want %s", gotPaths, want)
		}
	}
}

func TestAdmissible(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		host Instance
		want bool
	}{
		{"Fresh", Instance{Created: now.Add(-time.Hour)}, true},
		{"PastCutoff", Instance{Created: now.Add(-25 * time.Hour)}, false},
		{"UnknownAge", Instance{}, false},
		{"Retired", Instance{Created: now, Labels: map[string]string{hostLabelRetiredKey: "1"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admissible(tc.host, now, hostMaxAge); got != tc.want {
				t.Errorf("admissible = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestRetire_LosesToAConcurrentLabelWrite(t *testing.T) {
	gce := NewMemoryGCE()
	now := time.Now().UTC()
	gce.SeedRunning("z", "h1", hostLabels(schema.MachineClassStandard), "10.0.0.1", now)
	host, err := gce.GetInstance(context.Background(), "z", "h1")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if err := retire(context.Background(), gce, host, now); err != nil {
		t.Fatalf("retire: %v", err)
	}
	after, _ := gce.GetInstance(context.Background(), "z", "h1")
	if _, ok := after.Labels[hostLabelRetiredKey]; !ok {
		t.Errorf("labels = %v; want the retired stamp", after.Labels)
	}
	// The stale fingerprint makes a second swap lose.
	if err := retire(context.Background(), gce, host, now); err == nil {
		t.Error("retire with a stale fingerprint = nil; want it to lose the race")
	}
	// Retiring an already-retired host is a no-op, so a fresh read is free.
	if err := retire(context.Background(), gce, after, now); err != nil {
		t.Errorf("retire of an already-retired host = %v; want a no-op", err)
	}
}
