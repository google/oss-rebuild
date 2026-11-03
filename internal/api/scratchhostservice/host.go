// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"context"
	"io"
	"log"
	"os"
	"slices"
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
	ErrFull        = errors.New("host full")     // no slot is free
	ErrClosed      = errors.New("host closed")   // shutting down
	ErrNoGuest     = errors.New("no such guest") // nothing served for that scratch
	ErrGuestExited = errors.New("guest exited")  // process ended without a delete
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

// Host is the broker's state: its guest slots and every session's exec
// ledger. Lifecycle handlers hold the mutex across their steps. Each guest's
// process is the authority on its slot, so a guest that dies on its own
// frees it.
type Host struct {
	mu         sync.Mutex
	closed     bool
	lastServed time.Time        // last admit or loss, else startup
	slots      []slot           // capacity. The zero slot is free
	exited     map[string]*exit // until deleted or readmitted

	ledgerMu sync.Mutex         // op reads never wait on a boot
	ledgers  map[string]*Ledger // by scratch id, until deletion
}

// slot is one guest's place on the host. Its index names the guest's tap,
// address and directory.
type slot struct {
	guest *Guest             // nil when the slot is free
	proc  execx.Handle       // the guest's QEMU process
	stop  context.CancelFunc // the only way to end the guest
}

// exit records a guest that ended without a delete, so the readiness probe
// reports the death instead of timing out.
type exit struct {
	reason string // the process's exit error
}

// NewHost returns an empty Host of slots guest slots, last served at start.
func NewHost(start time.Time, slots int) *Host {
	return &Host{lastServed: start.UTC(), slots: make([]slot, slots), exited: map[string]*exit{}, ledgers: map[string]*Ledger{}}
}

// Close refuses further admission and stops every guest, giving QEMU its
// TERM. Taps and disks stay, and the next boot clears or reuses them.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for i, s := range h.slots {
		if s.guest != nil {
			s.stop()
			<-s.proc.Done()
			h.slots[i] = slot{}
		}
	}
}

// live clears the slots whose guest process has ended, recording each as
// exited. Must hold the mutex.
func (h *Host) live(now time.Time) {
	for i, s := range h.slots {
		if s.guest == nil {
			continue
		}
		select {
		case <-s.proc.Done():
		default:
			continue
		}
		reason := "exit status 0"
		if err := s.proc.Wait(); err != nil {
			reason = err.Error()
		}
		log.Printf("guest for %s exited on its own: %s", s.guest.ScratchID, reason)
		h.exited[s.guest.ScratchID] = &exit{reason: reason}
		s.stop() // release the dead guest's context
		h.slots[i] = slot{}
		h.lastServed = now
	}
}

// find returns the slot serving scratchID, or -1. Must hold the mutex.
func (h *Host) find(scratchID string) int {
	return slices.IndexFunc(h.slots, func(s slot) bool { return s.guest != nil && s.guest.ScratchID == scratchID })
}

// liveGuest returns the live guest serving scratchID, or ErrNoGuest.
func (h *Host) liveGuest(now time.Time, scratchID string) (Guest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live(now)
	if i := h.find(scratchID); i >= 0 {
		return *h.slots[i].guest, nil
	}
	if e, ok := h.exited[scratchID]; ok {
		return Guest{}, errors.Wrapf(ErrGuestExited, "%s: %s", scratchID, e.reason)
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
	now := time.Now().UTC()
	h.live(now)
	if i := h.find(req.ScratchID); i >= 0 {
		live := *h.slots[i].guest
		return &live, nil
	}
	i := slices.IndexFunc(h.slots, func(s slot) bool { return s.guest == nil })
	if i < 0 {
		return nil, api.AsStatus(codes.ResourceExhausted, errors.Wrapf(ErrFull, "all %d slots serving", len(h.slots)))
	}
	delete(h.exited, req.ScratchID)
	g := Guest{ScratchID: req.ScratchID, IP: deps.Config.Net.IPs[i].String(), Since: now}
	// The guest outlives this request.
	guestCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	proc, err := boot(ctx, guestCtx, deps, i)
	if err != nil {
		teardown(context.Background(), deps, i, proc, stop, g.IP)
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "booting guest for %s", req.ScratchID))
	}
	if err := h.openLedger(req.ScratchID, deps.Config.Layout.execDir(req.ScratchID), deps.Config.Guest.MaxOutputBytes); err != nil {
		teardown(context.Background(), deps, i, proc, stop, g.IP)
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "exec ledger for %s", req.ScratchID))
	}
	h.slots[i] = slot{guest: &g, proc: proc, stop: stop}
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
	if i := h.find(req.ScratchID); i >= 0 {
		s := h.slots[i]
		found = true
		err = teardown(ctx, deps, i, s.proc, s.stop, s.guest.IP)
		h.slots[i] = slot{}
		h.lastServed = time.Now().UTC()
	}
	if h.discardLedger(req.ScratchID) {
		found = true
	}
	delete(h.exited, req.ScratchID)
	if !found {
		return nil, api.AsStatus(codes.NotFound, errors.Wrapf(ErrNoGuest, "%s", req.ScratchID))
	}
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrapf(err, "tearing down guest for %s", req.ScratchID))
	}
	return &act.NoOutput{}, nil
}

// Guests reports the live guests.
func Guests(_ context.Context, _ ListRequest, deps *GuestDeps) (*GuestList, error) {
	h := deps.Host
	h.mu.Lock()
	defer h.mu.Unlock()
	// Liveness first: noticing a death moves lastServed.
	h.live(time.Now().UTC())
	out := &GuestList{Guests: []Guest{}, Slots: len(h.slots), LastServed: h.lastServed}
	for _, s := range h.slots {
		if s.guest != nil {
			out.Guests = append(out.Guests, *s.guest)
		}
	}
	return out, nil
}

// GuestHealth runs a command in the session's guest, so it fails until the
// guest's sshd is up. Failures are Unavailable, so the caller polls until
// the guest is ready. A guest that has exited fails with FailedPrecondition
// instead, which ends the polling.
func GuestHealth(ctx context.Context, req GuestRequest, deps *GuestDeps) (*act.NoOutput, error) {
	g, err := deps.Host.liveGuest(time.Now().UTC(), req.ScratchID)
	if errors.Is(err, ErrGuestExited) {
		return nil, api.AsStatus(codes.FailedPrecondition, err)
	}
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

// boot creates slot i's disks, tap and guest process, in that order.
// guestCtx bounds the guest's life. On failure the caller tears the slot
// down, and the handle is nil unless every step succeeded.
func boot(ctx, guestCtx context.Context, deps *GuestDeps, i int) (execx.Handle, error) {
	cfg := deps.Config
	l := cfg.Layout
	// A guest that died on its own leaves its disks, and ensureTap reuses its tap.
	if err := os.RemoveAll(l.slotDir(i)); err != nil {
		return nil, errors.Wrap(err, "clearing guest dir")
	}
	if err := os.MkdirAll(l.slotDir(i), 0o755); err != nil {
		return nil, errors.Wrap(err, "guest dir")
	}
	// Metadata-only overlays over the shared read-only images.
	if err := createOverlay(ctx, deps.Exec, cfg.QEMUImgBin, l.overlay(i), l.Rootfs); err != nil {
		return nil, errors.Wrap(err, "root overlay")
	}
	if err := createOverlay(ctx, deps.Exec, cfg.QEMUImgBin, l.scratch(i), l.Scratch); err != nil {
		return nil, errors.Wrap(err, "scratch overlay")
	}
	if err := ensureTap(ctx, deps.Exec, guestTap(i), cfg.Bridge, cfg.Guest.MTU); err != nil {
		return nil, errors.Wrap(err, "guest tap")
	}
	credential, err := deps.ChannelCache.GuestCredential(cfg.Net.IPs[i].String())
	if err != nil {
		return nil, errors.Wrap(err, "guest credential")
	}
	// QEMU's stderr is the only record of a boot that never reached the console log.
	argv := qemuArgv(cfg.QEMUBin, cfg.Guest, l, cfg.Net, i, credential)
	proc, err := deps.Exec.Start(guestCtx, execx.CommandOptions{Output: os.Stderr, StopGrace: stopGrace}, argv[0], argv[1:]...)
	if err != nil {
		return nil, errors.Wrap(err, "starting guest")
	}
	return proc, nil
}

// teardown closes slot i's channel, stops its guest, and removes its tap
// and disks. Every step runs even if an earlier one fails. proc is nil when
// the guest never started.
func teardown(ctx context.Context, deps *GuestDeps, i int, proc execx.Handle, stop context.CancelFunc, ip string) error {
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
	if err := removeTap(ctx, deps.Exec, guestTap(i)); err != nil {
		fail(err, "removing tap")
	}
	if err := os.RemoveAll(deps.Config.Layout.slotDir(i)); err != nil {
		fail(err, "removing guest dir")
	}
	return firstErr
}
