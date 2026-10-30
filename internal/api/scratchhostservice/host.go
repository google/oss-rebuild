// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/execx"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// Errors the handlers map to status codes.
var (
	ErrFull    = errors.New("host full")     // no slot is free
	ErrClosed  = errors.New("host closed")   // shutting down
	ErrNoGuest = errors.New("no such guest") // nothing served for that scratch
)

// Config is the host's static configuration.
type Config struct {
	Layout     Layout
	Guest      GuestConfig
	Net        GuestNet
	Bridge     string // host bridge the guest tap attaches to
	QEMUBin    string // resolved through PATH
	QEMUImgBin string // resolved through PATH
}

// Host is the broker's state: its one guest slot and every session's exec
// ledger. Lifecycle handlers hold the mutex across their steps. The guest's
// process is the authority on the slot, so a guest that dies on its own
// frees it.
type Host struct {
	mu         sync.Mutex
	closed     bool
	lastServed time.Time          // last guest, or else startup
	guest      *Guest             // nil when the slot is free
	proc       execx.Handle       // the guest's QEMU process
	stop       context.CancelFunc // the only way to end the guest

	ledgerMu sync.Mutex         // op reads never wait on a boot
	ledgers  map[string]*Ledger // by scratch id, until deletion
}

// NewHost returns an empty Host that reads as last served at start.
func NewHost(start time.Time) *Host {
	return &Host{lastServed: start.UTC(), ledgers: map[string]*Ledger{}}
}

// Close refuses further admission and stops the guest, giving QEMU its
// TERM. The tap and disks stay, and the next boot clears or reuses them.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.guest != nil {
		h.stop()
		<-h.proc.Done()
		h.guest, h.proc, h.stop = nil, nil, nil
	}
}

// live returns the slot's guest if its process runs, and clears the slot
// otherwise. Must hold the mutex.
func (h *Host) live(now time.Time) *Guest {
	if h.guest == nil {
		return nil
	}
	select {
	case <-h.proc.Done():
		log.Printf("guest for %s exited on its own: %v", h.guest.ScratchID, h.proc.Wait())
		h.stop() // release the dead guest's context
		h.guest, h.proc, h.stop = nil, nil, nil
		h.lastServed = now
		return nil
	default:
		return h.guest
	}
}

// liveGuest returns the live guest serving scratchID, or ErrNoGuest.
func (h *Host) liveGuest(now time.Time, scratchID string) (Guest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if g := h.live(now); g != nil && g.ScratchID == scratchID {
		return *g, nil
	}
	return Guest{}, errors.Wrapf(ErrNoGuest, "%s", scratchID)
}

// ledger returns the session's exec ledger.
func (h *Host) ledger(scratchID string) (*Ledger, bool) {
	h.ledgerMu.Lock()
	defer h.ledgerMu.Unlock()
	l, ok := h.ledgers[scratchID]
	return l, ok
}

// ledgerFor returns the ledger holding opID. Op ids are unique across
// sessions.
func (h *Host) ledgerFor(opID string) (*Ledger, bool) {
	h.ledgerMu.Lock()
	defer h.ledgerMu.Unlock()
	for _, l := range h.ledgers {
		if l.has(opID) {
			return l, true
		}
	}
	return nil, false
}

// openLedger replaces the session's ledger with an empty one at dir.
func (h *Host) openLedger(scratchID, dir string, maxOutput int64) error {
	h.discardLedger(scratchID)
	l, err := NewLedger(dir, maxOutput)
	if err != nil {
		return err
	}
	h.ledgerMu.Lock()
	defer h.ledgerMu.Unlock()
	h.ledgers[scratchID] = l
	return nil
}

// discardLedger drops the session's ledger and its files, reporting
// whether there was one.
func (h *Host) discardLedger(scratchID string) bool {
	h.ledgerMu.Lock()
	l, ok := h.ledgers[scratchID]
	delete(h.ledgers, scratchID)
	h.ledgerMu.Unlock()
	if ok {
		l.Discard()
	}
	return ok
}

// GuestDeps wires the guest lifecycle handlers.
type GuestDeps struct {
	Host         *Host
	Config       Config
	Exec         execx.CommandExecutor // runs the guest, qemu-img and ip(8)
	ChannelCache *ChannelCache         // guest exec paths and boot key
}

// GuestAdmit boots the session's guest and opens its exec ledger.
// Idempotent on scratch id. ResourceExhausted means the host is busy and
// Unavailable that it is closing. Either way the caller tries another host.
func GuestAdmit(ctx context.Context, req AdmitRequest, deps *GuestDeps) (*Guest, error) {
	h := deps.Host
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, api.AsStatus(codes.Unavailable, ErrClosed)
	}
	if g := h.live(time.Now().UTC()); g != nil {
		if g.ScratchID == req.ScratchID {
			live := *g
			return &live, nil
		}
		return nil, api.AsStatus(codes.ResourceExhausted, errors.Wrapf(ErrFull, "serving %s", g.ScratchID))
	}
	g := Guest{ScratchID: req.ScratchID, IP: deps.Config.Net.IP.String(), Since: time.Now().UTC()}
	// The guest outlives this request.
	guestCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	proc, err := boot(ctx, guestCtx, deps)
	if err != nil {
		teardown(context.Background(), deps, proc, stop, g.IP)
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "booting guest for %s", req.ScratchID))
	}
	if err := h.openLedger(req.ScratchID, deps.Config.Layout.execDir(req.ScratchID), deps.Config.Guest.MaxOutputBytes); err != nil {
		teardown(context.Background(), deps, proc, stop, g.IP)
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "exec ledger for %s", req.ScratchID))
	}
	h.guest, h.proc, h.stop = &g, proc, stop
	h.lastServed = g.Since
	out := g
	return &out, nil
}

// GuestDelete tears down the session's guest and exec records, which the
// caller must sync first. NotFound when there is nothing to tear down,
// which callers treat as success.
func GuestDelete(ctx context.Context, req GuestRequest, deps *GuestDeps) (*act.NoOutput, error) {
	h := deps.Host
	h.mu.Lock()
	defer h.mu.Unlock()
	var (
		found bool
		err   error
	)
	// Dead guests are torn down too: their disks and tap remain.
	if h.guest != nil && h.guest.ScratchID == req.ScratchID {
		found = true
		err = teardown(ctx, deps, h.proc, h.stop, h.guest.IP)
		h.guest, h.proc, h.stop = nil, nil, nil
		h.lastServed = time.Now().UTC()
	}
	if h.discardLedger(req.ScratchID) {
		found = true
	}
	if !found {
		return nil, api.AsStatus(codes.NotFound, errors.Wrapf(ErrNoGuest, "%s", req.ScratchID))
	}
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "tearing down guest for %s", req.ScratchID))
	}
	return &act.NoOutput{}, nil
}

// Guests reports the live guest.
func Guests(_ context.Context, _ ListRequest, deps *GuestDeps) (*GuestList, error) {
	h := deps.Host
	h.mu.Lock()
	defer h.mu.Unlock()
	// Liveness first: noticing a death moves lastServed.
	g := h.live(time.Now().UTC())
	out := &GuestList{Guests: []Guest{}, LastServed: h.lastServed}
	if g != nil {
		out.Guests = append(out.Guests, *g)
	}
	return out, nil
}

// GuestHealth runs a command in the session's guest, so it fails until the
// guest's sshd is up. Failures are Unavailable, so the caller polls until
// the guest is ready.
func GuestHealth(ctx context.Context, req GuestRequest, deps *GuestDeps) (*act.NoOutput, error) {
	g, err := deps.Host.liveGuest(time.Now().UTC(), req.ScratchID)
	if err != nil {
		return nil, api.AsStatus(codes.Unavailable, err)
	}
	code, err := deps.ChannelCache.run(ctx, g.IP, Command{Argv: []string{"true"}}, io.Discard)
	if err != nil {
		return nil, api.AsStatus(codes.Unavailable, errors.Wrapf(err, "probing guest %s", req.ScratchID))
	}
	if code != 0 {
		return nil, api.AsStatus(codes.Unavailable, errors.Errorf("guest %s exec probe exited %d", req.ScratchID, code))
	}
	return &act.NoOutput{}, nil
}

// boot creates the guest's disks, tap and process, in that order. guestCtx
// bounds the guest's life. On failure the caller tears the slot down, and
// the handle is nil unless every step succeeded.
func boot(ctx, guestCtx context.Context, deps *GuestDeps) (execx.Handle, error) {
	cfg := deps.Config
	l := cfg.Layout
	// A guest that died on its own leaves its disks, and ensureTap reuses its tap.
	if err := os.RemoveAll(l.GuestDir); err != nil {
		return nil, errors.Wrap(err, "clearing guest dir")
	}
	if err := os.MkdirAll(l.GuestDir, 0o755); err != nil {
		return nil, errors.Wrap(err, "guest dir")
	}
	// Metadata-only overlays over the shared read-only images.
	if err := createOverlay(ctx, deps.Exec, cfg.QEMUImgBin, l.overlay(), l.Rootfs); err != nil {
		return nil, errors.Wrap(err, "root overlay")
	}
	if err := createOverlay(ctx, deps.Exec, cfg.QEMUImgBin, l.scratch(), l.Scratch); err != nil {
		return nil, errors.Wrap(err, "scratch overlay")
	}
	if err := ensureTap(ctx, deps.Exec, guestTap, cfg.Bridge, cfg.Guest.MTU); err != nil {
		return nil, errors.Wrap(err, "guest tap")
	}
	// QEMU's stderr is the only record of a boot that never reached the console log.
	argv := qemuArgv(cfg.QEMUBin, cfg.Guest, l, cfg.Net, deps.ChannelCache.GuestCredential())
	proc, err := deps.Exec.Start(guestCtx, execx.CommandOptions{Output: os.Stderr, StopGrace: stopGrace}, argv[0], argv[1:]...)
	if err != nil {
		return nil, errors.Wrap(err, "starting guest")
	}
	return proc, nil
}

// teardown closes the guest's channel, stops it, and removes its tap and
// disks. Every step runs even if an earlier one fails. proc is nil when
// the guest never started.
func teardown(ctx context.Context, deps *GuestDeps, proc execx.Handle, stop context.CancelFunc, ip string) error {
	var firstErr error
	fail := func(err error, what string) {
		log.Printf("teardown: %s: %v", what, err)
		if firstErr == nil {
			firstErr = errors.Wrap(err, what)
		}
	}
	deps.ChannelCache.close(ip)
	stop()
	if proc != nil {
		<-proc.Done()
	}
	if err := removeTap(ctx, deps.Exec, guestTap); err != nil {
		fail(err, "removing tap")
	}
	if err := os.RemoveAll(deps.Config.Layout.GuestDir); err != nil {
		fail(err, "removing guest dir")
	}
	return firstErr
}
