// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/execx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	errNoTap = errors.New(`Device "mvmtap0" does not exist`)
)

// testDeps returns GuestDeps over a mock executor. The mock records
// commands, runs the guest until stopped, answers ip(8) tap queries and
// creates the overlays qemu-img would. Each guest comes up on a guestNet
// while its QEMU runs and echoes its commands' argv. Tests that read the
// clock run in a synctest bubble.
func testDeps(t *testing.T) (*GuestDeps, *execx.MockCommandExecutor) {
	t.Helper()
	root := t.TempDir()
	guestNet, err := GuestNetFor(netip.MustParsePrefix("10.200.0.0/24"))
	if err != nil {
		t.Fatalf("GuestNetFor: %v", err)
	}
	mock := execx.NewMockCommandExecutor()
	var (
		mu  sync.Mutex
		tap bool
	)
	mock.SetExecuteFunc(func(_ context.Context, _ execx.CommandOptions, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		switch strings.Join(append([]string{name}, args...), " ") {
		case "ip link show mvmtap0":
			if !tap {
				return errNoTap
			}
		case "ip tuntap add dev mvmtap0 mode tap":
			tap = true
		case "ip link del mvmtap0":
			tap = false
		}
		if name == "qemu-img" {
			return os.WriteFile(args[len(args)-1], nil, 0o644)
		}
		return nil
	})
	deps := &GuestDeps{
		Host: NewHost(time.Now()),
		Config: Config{
			Layout: Layout{
				GuestDir: filepath.Join(root, "guest"),
				ExecsDir: filepath.Join(root, "execs"),
				Kernel:   "/golden/vmlinuz",
				Initrd:   "/golden/initrd.img",
				Rootfs:   "/golden/rootfs.raw",
				Scratch:  "/golden/scratch.raw",
			},
			Guest:      GuestConfig{VCPU: 4, MemMB: 8192, DNS: "169.254.169.254", MTU: 1460, Workdir: "/home/builder"},
			Net:        guestNet,
			Bridge:     "mvmbr0",
			QEMUBin:    "qemu-system-x86_64",
			QEMUImgBin: "qemu-img",
		},
		Exec: mock,
	}
	guestsOn(t, deps)
	t.Cleanup(func() {
		deps.Host.mu.Lock()
		p, stop := deps.Host.proc, deps.Host.stop
		deps.Host.mu.Unlock()
		if p != nil {
			stop()
			<-p.Done()
		}
	})
	return deps, mock
}

// keyParam matches the ssh keys on a kernel command line, which differ per
// run.
var keyParam = regexp.MustCompile(`(mvm_ssh_[a-z_]+_key)=\S+`)

// commands renders the recorded commands as lines, for exact comparison, with
// the ssh keys masked.
func commands(mock *execx.MockCommandExecutor) []string {
	var out []string
	for _, c := range mock.GetCommands() {
		out = append(out, keyParam.ReplaceAllString(strings.Join(append([]string{c.Name}, c.Args...), " "), "$1=*"))
	}
	return out
}

// bootCommands is what one boot into an empty slot issues.
func bootCommands(deps *GuestDeps) []string {
	cfg := deps.Config
	l := cfg.Layout
	return []string{
		"qemu-img create -q -f qcow2 -F raw -b /golden/rootfs.raw " + l.overlay(),
		"qemu-img create -q -f qcow2 -F raw -b /golden/scratch.raw " + l.scratch(),
		"ip link show mvmtap0",
		"ip tuntap add dev mvmtap0 mode tap",
		"ip link set mvmtap0 mtu 1460",
		"ip link set mvmtap0 master mvmbr0",
		"ip link set mvmtap0 up",
		strings.Join(qemuArgv(cfg.QEMUBin, cfg.Guest, l, cfg.Net, "*"), " "),
	}
}

func admit(t *testing.T, deps *GuestDeps, scratchID string) Guest {
	t.Helper()
	g, err := GuestAdmit(context.Background(), AdmitRequest{ScratchID: scratchID}, deps)
	if err != nil {
		t.Fatalf("GuestAdmit(%s): %v", scratchID, err)
	}
	return *g
}

func guestProc(t *testing.T, deps *GuestDeps) execx.Handle {
	t.Helper()
	deps.Host.mu.Lock()
	defer deps.Host.mu.Unlock()
	if deps.Host.proc == nil {
		t.Fatal("no guest process in the slot")
	}
	return deps.Host.proc
}

func running(h execx.Handle) bool {
	select {
	case <-h.Done():
		return false
	default:
		return true
	}
}

// guestsOn puts deps' guests on a fresh guestNet for the test to script. A
// guest is up while the QEMU last started for its address runs, with the keys
// from that QEMU's kernel command line.
func guestsOn(t *testing.T, deps *GuestDeps) *guestNet {
	t.Helper()
	mock := deps.Exec.(*execx.MockCommandExecutor)
	n := newGuestNet(t)
	n.boots = func(addr string) (string, <-chan struct{}, bool) {
		cmds := mock.GetCommands()
		for i := len(cmds) - 1; i >= 0; i-- {
			c := cmds[i]
			at := slices.Index(c.Args, "-append")
			if c.Name != deps.Config.QEMUBin || c.Handle == nil || at < 0 || at+1 == len(c.Args) {
				continue
			}
			cmdline := c.Args[at+1]
			if !slices.Contains(strings.Fields(cmdline), "mvm_ip="+addr) {
				continue
			}
			select {
			case <-c.Handle.Done():
				return "", nil, false
			default:
				return cmdline, c.Handle.Done(), true
			}
		}
		return "", nil, false
	}
	deps.ChannelCache = NewChannelCache(testDialer(t, n))
	return n
}

// killGuest ends the guest as a crashed QEMU would, without telling the
// broker.
func killGuest(t *testing.T, deps *GuestDeps) {
	t.Helper()
	cmds := deps.Exec.(*execx.MockCommandExecutor).GetCommands()
	var h *execx.MockHandle
	for i := len(cmds) - 1; i >= 0 && h == nil; i-- {
		h = cmds[i].Handle
	}
	if h == nil {
		t.Fatal("no started guest to kill")
	}
	h.Exit(errors.New("signal: killed"))
}

func guestDirExists(deps *GuestDeps) bool {
	_, err := os.Stat(deps.Config.Layout.GuestDir)
	return err == nil
}

func TestGuestAdmit_BootsInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, mock := testDeps(t)
		g := admit(t, deps, "scratch-a")
		if diff := cmp.Diff(Guest{ScratchID: "scratch-a", IP: "10.200.0.2", Since: time.Now().UTC()}, g); diff != "" {
			t.Errorf("guest mismatch (-want +got):\n%s", diff)
		}
		// Disks, tap, then the guest with the pinned argv.
		want := bootCommands(deps)
		if diff := cmp.Diff(want, commands(mock)); diff != "" {
			t.Errorf("boot commands mismatch (-want +got):\n%s", diff)
		}
		if !running(guestProc(t, deps)) {
			t.Error("guest process not running after admit")
		}
	})
}

func TestGuestAdmit_Idempotent(t *testing.T) {
	deps, mock := testDeps(t)
	first := admit(t, deps, "scratch-a")
	p := guestProc(t, deps)
	issued := len(commands(mock))
	second := admit(t, deps, "scratch-a")
	if first != second {
		t.Errorf("re-admission = %+v; want the existing guest %+v", second, first)
	}
	if guestProc(t, deps) != p || len(commands(mock)) != issued {
		t.Error("re-admission touched the guest; want it left alone")
	}
}

func TestGuestAdmit_FullWhileServing(t *testing.T) {
	deps, _ := testDeps(t)
	ctx := context.Background()
	a := admit(t, deps, "scratch-a")
	if _, err := GuestAdmit(ctx, AdmitRequest{ScratchID: "scratch-b"}, deps); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("Admit while serving = %v; want ResourceExhausted", err)
	}
	// The slot is reusable once its session is deleted.
	if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Fatalf("GuestDelete: %v", err)
	}
	if b := admit(t, deps, "scratch-b"); b.IP != a.IP {
		t.Errorf("next guest at %s; want the freed address %s", b.IP, a.IP)
	}
}

func TestGuestAdmit_ClosedAfterShutdown(t *testing.T) {
	deps, _ := testDeps(t)
	deps.Host.Close()
	_, err := GuestAdmit(context.Background(), AdmitRequest{ScratchID: "scratch-a"}, deps)
	// Closed is terminal for the host, unlike full.
	if status.Code(err) != codes.Unavailable {
		t.Errorf("Admit after Close = %v; want Unavailable", err)
	}
}

func TestHostClose_EndsTheGuest(t *testing.T) {
	deps, _ := testDeps(t)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	p := guestProc(t, deps)
	deps.Host.Close()
	if running(p) {
		t.Error("guest process survived Close")
	}
	if list, _ := Guests(ctx, ListRequest{}, deps); len(list.Guests) != 0 {
		t.Errorf("guests after Close = %+v; want none", list.Guests)
	}
	if _, err := GuestAdmit(ctx, AdmitRequest{ScratchID: "scratch-b"}, deps); status.Code(err) != codes.Unavailable {
		t.Errorf("Admit after Close = %v; want Unavailable", err)
	}
}

func TestGuestAdmit_BootFailureLeavesNoSlotHeld(t *testing.T) {
	deps, mock := testDeps(t)
	mock.SetStartFunc(func(context.Context, execx.CommandOptions, string, ...string) (execx.Handle, error) {
		return nil, errors.New("exec: qemu-system-x86_64: not found")
	})
	if _, err := GuestAdmit(context.Background(), AdmitRequest{ScratchID: "scratch-a"}, deps); status.Code(err) != codes.Internal {
		t.Fatalf("Admit with a guest that cannot start = %v; want Internal", err)
	}
	// Everything the failed boot created is torn down again.
	want := append(bootCommands(deps), "ip link show mvmtap0", "ip link del mvmtap0")
	if diff := cmp.Diff(want, commands(mock)); diff != "" {
		t.Errorf("commands mismatch (-want +got):\n%s", diff)
	}
	if guestDirExists(deps) {
		t.Error("failed boot left the guest dir behind")
	}
	mock.SetStartFunc(nil)
	admit(t, deps, "scratch-b")
}

func TestGuestAdmit_ReclaimsADeadGuestsSlot(t *testing.T) {
	deps, mock := testDeps(t)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	// The guest dies unannounced. The next admission reclaims its leftovers.
	killGuest(t, deps)
	mock.Reset()
	b := admit(t, deps, "scratch-b")
	// The dead guest's tap is reused, so only the tuntap add drops out.
	want := slices.DeleteFunc(bootCommands(deps), func(c string) bool { return strings.HasPrefix(c, "ip tuntap add") })
	if diff := cmp.Diff(want, commands(mock)); diff != "" {
		t.Errorf("reboot commands mismatch (-want +got):\n%s", diff)
	}
	list, err := Guests(ctx, ListRequest{}, deps)
	if err != nil {
		t.Fatalf("Guests: %v", err)
	}
	if diff := cmp.Diff([]Guest{b}, list.Guests); diff != "" {
		t.Errorf("guests mismatch (-want +got):\n%s", diff)
	}
	// Deleting the dead session discards only its ledger.
	if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Errorf("Delete(scratch-a) after its guest died = %v; want its records reclaimed", err)
	}
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-b"}, deps); err != nil {
		t.Errorf("GuestHealth(scratch-b) = %v; want the live guest untouched", err)
	}
}

func TestGuestDelete_ReleasesEverythingAndIsIdempotent(t *testing.T) {
	deps, mock := testDeps(t)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	p := guestProc(t, deps)
	mock.Reset()
	if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Fatalf("GuestDelete: %v", err)
	}
	if running(p) {
		t.Error("guest process survived Delete")
	}
	if guestDirExists(deps) {
		t.Error("guest dir survived Delete")
	}
	if diff := cmp.Diff([]string{"ip link show mvmtap0", "ip link del mvmtap0"}, commands(mock)); diff != "" {
		t.Errorf("teardown commands mismatch (-want +got):\n%s", diff)
	}
	if list, _ := Guests(ctx, ListRequest{}, deps); len(list.Guests) != 0 {
		t.Errorf("guests after Delete = %+v; want none", list.Guests)
	}
	if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); status.Code(err) != codes.NotFound {
		t.Errorf("second Delete = %v; want NotFound, which the caller reads as success", err)
	}
}

func TestGuestDelete_LeavesNoChannelForTheNextGuest(t *testing.T) {
	deps, _ := testDeps(t)
	n := guestsOn(t, deps)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Fatalf("GuestHealth: %v", err)
	}
	if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Fatalf("GuestDelete: %v", err)
	}
	// The next guest boots at the same address. Its first probe must not
	// land on the dead guest's connection.
	admit(t, deps, "scratch-b")
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-b"}, deps); err != nil {
		t.Errorf("first GuestHealth of the next guest = %v; want ready", err)
	}
	if dials := n.dialCount(); dials != 2 {
		t.Errorf("%d dials; want one per guest", dials)
	}
}

func TestGuestHealth_ProbesOverTheChannel(t *testing.T) {
	deps, _ := testDeps(t)
	n := guestsOn(t, deps)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
		t.Errorf("GuestHealth of an answering guest = %v; want ready", err)
	}
	if got := n.ran(); len(got) != 1 || !slices.Equal(got[0].Argv, []string{"true"}) {
		t.Errorf("guest ran %+v; want the probe to run true in the guest", got)
	}
	// The probe fails until the guest's sshd is up.
	n.setRefuse(true)
	deps.ChannelCache.close("10.200.0.2")
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); status.Code(err) != codes.Unavailable {
		t.Errorf("GuestHealth with an unreachable guest = %v; want Unavailable", err)
	}
	// A guest that answers but cannot run the probe is not ready either.
	n.setRefuse(false)
	n.setRun(func(context.Context, Command, io.Writer, io.Writer) guestExit { return guestExit{status: 127} })
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); status.Code(err) != codes.Unavailable {
		t.Errorf("GuestHealth with a failing probe = %v; want Unavailable", err)
	}
	// And neither is a session this host never admitted.
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-zzz"}, deps); status.Code(err) != codes.Unavailable {
		t.Errorf("GuestHealth of an unknown session = %v; want Unavailable", err)
	}
}

func TestGuests_DerivedFromTheProcess(t *testing.T) {
	deps, _ := testDeps(t)
	ctx := context.Background()
	admit(t, deps, "scratch-a")
	killGuest(t, deps)
	if list, _ := Guests(ctx, ListRequest{}, deps); len(list.Guests) != 0 {
		t.Errorf("guests = %+v; want a dead guest not counted", list.Guests)
	}
	if _, err := GuestHealth(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); status.Code(err) != codes.Unavailable {
		t.Errorf("GuestHealth of a dead guest = %v; want Unavailable", err)
	}
}

func TestLastServed_TracksTeardownAndDeath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The empty-host reap measures idleness from LastServed, so it must move at
		// teardown, not only at admission.
		deps, _ := testDeps(t)
		ctx := context.Background()
		admit(t, deps, "scratch-a")
		time.Sleep(2 * time.Hour)
		torndown := time.Now().UTC()
		if _, err := GuestDelete(ctx, GuestRequest{ScratchID: "scratch-a"}, deps); err != nil {
			t.Fatalf("GuestDelete: %v", err)
		}
		if list, _ := Guests(ctx, ListRequest{}, deps); !list.LastServed.Equal(torndown) {
			t.Errorf("LastServed after Delete = %v; want the teardown time %v", list.LastServed, torndown)
		}
		admit(t, deps, "scratch-b")
		time.Sleep(3 * time.Hour)
		noticed := time.Now().UTC()
		killGuest(t, deps)
		if list, _ := Guests(ctx, ListRequest{}, deps); !list.LastServed.Equal(noticed) {
			t.Errorf("LastServed after a guest death = %v; want when it was noticed, %v", list.LastServed, noticed)
		}
	})
}

func TestAdmitRequest_Validate(t *testing.T) {
	for _, tc := range []struct {
		id string
		ok bool
	}{
		{"scratch-a", true},
		{"5f1b3c2e_0001", true},
		{"", false},
		{"..", false},
		{"a/b", false},
		{"a%2Fb", false},
		{"a b", false},
		{strings.Repeat("a", 129), false},
	} {
		if err := (AdmitRequest{ScratchID: tc.id}).Validate(); (err == nil) != tc.ok {
			t.Errorf("Validate(%q) = %v; want ok %v", tc.id, err, tc.ok)
		}
	}
}

// serve stands up the lifecycle routes as main wires them and returns the
// base URL.
func serve(t *testing.T, deps *GuestDeps) *url.URL {
	t.Helper()
	guestInit := func(context.Context) (*GuestDeps, error) { return deps, nil }
	mux := http.NewServeMux()
	mux.Handle(RouteAdmit, api.Handler(guestInit, GuestAdmit))
	mux.Handle(RouteDelete, api.Handler(guestInit, GuestDelete))
	mux.Handle(RouteList, api.Handler(guestInit, Guests))
	mux.Handle(RouteGuestHealth, api.Handler(guestInit, GuestHealth))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	return u
}

// TestGuestRoutes_OverTheWire runs a guest's lifecycle through agent-api's
// stubs, checking the status code each step returns.
func TestGuestRoutes_OverTheWire(t *testing.T) {
	deps, _ := testDeps(t)
	base := serve(t, deps)
	admitStub := api.Stub[AdmitRequest, Guest](http.DefaultClient, base.JoinPath(RouteAdmit))
	deleteStub := api.Stub[GuestRequest, act.NoOutput](http.DefaultClient, base.JoinPath(RouteDelete))
	listStub := api.Stub[ListRequest, GuestList](http.DefaultClient, base.JoinPath(RouteList))
	healthStub := api.Stub[GuestRequest, act.NoOutput](http.DefaultClient, base.JoinPath(RouteGuestHealth))
	ctx := context.Background()
	a := GuestRequest{ScratchID: "scratch-a"}
	admitAfterClose := func() error {
		deps.Host.Close()
		return errOf(admitStub(ctx, AdmitRequest{ScratchID: "scratch-c"}))
	}
	for _, step := range []struct {
		name string
		call func() error
		want codes.Code
	}{
		{"admit", func() error { return errOf(admitStub(ctx, AdmitRequest{ScratchID: "scratch-a"})) }, codes.OK},
		{"health", func() error { return errOf(healthStub(ctx, a)) }, codes.OK},
		{"list", func() error { return errOf(listStub(ctx, ListRequest{})) }, codes.OK},
		{"admit while serving", func() error { return errOf(admitStub(ctx, AdmitRequest{ScratchID: "scratch-b"})) }, codes.ResourceExhausted},
		{"delete", func() error { return errOf(deleteStub(ctx, a)) }, codes.OK},
		{"repeat delete", func() error { return errOf(deleteStub(ctx, a)) }, codes.NotFound},
		{"health after delete", func() error { return errOf(healthStub(ctx, a)) }, codes.Unavailable},
		{"admit after close", admitAfterClose, codes.Unavailable},
	} {
		if got := status.Code(step.call()); got != step.want {
			t.Errorf("%s = %v, want %v", step.name, got, step.want)
		}
	}
}

// errOf drops a stub's result.
func errOf[T any](_ *T, err error) error { return err }
