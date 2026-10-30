// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Command scratch-host is the scratch host broker. See package
// scratchhostservice.
//
// TODO: Serve the exec API.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/netip"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/oss-rebuild/internal/api/idauth"
	"github.com/google/oss-rebuild/internal/api/scratchhostservice"
	"github.com/google/oss-rebuild/internal/execx"
	"github.com/google/oss-rebuild/pkg/act/api"
)

var (
	callerSA = flag.String("caller-sa", "", "caller service account email (sender of incoming requests)")
	audience = flag.String("audience", "", "expected ID-token audience (e.g. https://builder/<host-name>)")
	listen   = flag.String("listen", ":8080", "listen address")

	guestDir  = flag.String("guest-dir", "/var/lib/scratch-host/guest", "directory holding the guest's disk overlays and console log")
	goldenDir = flag.String("golden-dir", "/var/lib/scratch-host/golden", "directory holding vmlinuz, initrd.img, rootfs.raw and scratch.raw")

	guestVCPU  = flag.Int("guest-vcpu", 4, "vCPUs for the guest")
	guestMemMB = flag.Int("guest-mem-mb", 12288, "memory for the guest, in MB")
	guestMTU   = flag.Int("guest-mtu", 1460, "guest MTU, at most the host NIC's")
	guestDNS   = flag.String("guest-dns", "169.254.169.254", "resolver written into the guest")

	bridge      = flag.String("bridge", "mvmbr0", "host bridge the guest tap attaches to")
	guestSubnet = flag.String("guest-subnet", "10.200.0.0/24", "host-only guest subnet, never reachable off this host")

	qemuBin    = flag.String("qemu-bin", "qemu-system-x86_64", "system emulator binary")
	qemuImgBin = flag.String("qemu-img-bin", "qemu-img", "qemu-img binary")
)

// The guest image's sshd authorizes the broker's key for root.
const (
	sshUser        = "root"
	sshPort        = 22
	sshDialTimeout = 10 * time.Second // also bounds the readiness probe
)

func main() {
	flag.Parse()
	if *callerSA == "" {
		log.Fatalf("--caller-sa is required")
	}
	if *audience == "" {
		log.Fatalf("--audience is required")
	}
	subnet, err := netip.ParsePrefix(*guestSubnet)
	if err != nil {
		log.Fatalf("parsing --guest-subnet: %v", err)
	}
	guestNet, err := scratchhostservice.GuestNetFor(subnet)
	if err != nil {
		log.Fatalf("--guest-subnet: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	dialer, err := scratchhostservice.NewSSHDialer(scratchhostservice.SSHConfig{
		User:        sshUser,
		Port:        sshPort,
		DialTimeout: sshDialTimeout,
	})
	if err != nil {
		log.Fatalf("exec channel: %v", err)
	}
	host := scratchhostservice.NewHost(time.Now())
	guestDeps := &scratchhostservice.GuestDeps{
		Host: host,
		Config: scratchhostservice.Config{
			Layout: scratchhostservice.Layout{
				GuestDir: *guestDir,
				Kernel:   filepath.Join(*goldenDir, "vmlinuz"),
				Initrd:   filepath.Join(*goldenDir, "initrd.img"),
				Rootfs:   filepath.Join(*goldenDir, "rootfs.raw"),
				Scratch:  filepath.Join(*goldenDir, "scratch.raw"),
			},
			Guest: scratchhostservice.GuestConfig{
				VCPU:  *guestVCPU,
				MemMB: *guestMemMB,
				DNS:   *guestDNS,
				MTU:   *guestMTU,
			},
			Net:        guestNet,
			Bridge:     *bridge,
			QEMUBin:    *qemuBin,
			QEMUImgBin: *qemuImgBin,
		},
		Exec:         execx.NewRealCommandExecutor(),
		ChannelCache: scratchhostservice.NewChannelCache(dialer),
	}
	guestInit := func(context.Context) (*scratchhostservice.GuestDeps, error) { return guestDeps, nil }
	mw := idauth.Middleware(idauth.NewGoogleValidator(*callerSA, *audience))
	mux := http.NewServeMux()
	mux.Handle(scratchhostservice.RouteAdmit, mw(api.Handler(guestInit, scratchhostservice.GuestAdmit)))
	mux.Handle(scratchhostservice.RouteDelete, mw(api.Handler(guestInit, scratchhostservice.GuestDelete)))
	mux.Handle(scratchhostservice.RouteList, mw(api.Handler(guestInit, scratchhostservice.Guests)))
	// The health checks are open. The guest probe runs a command in the guest,
	// so it is bounded like a dial.
	mux.Handle(scratchhostservice.RouteGuestHealth, api.Handler(guestInit, api.WithTimeout(sshDialTimeout, scratchhostservice.GuestHealth)))
	mux.HandleFunc(scratchhostservice.RouteHealthz, func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: *listen, Handler: mux}
	drained := make(chan struct{}) // ListenAndServe returns as Shutdown starts, not when it ends
	go func() {
		defer close(drained)
		<-ctx.Done()
		// Stop admitting and end guests before the listener drains, so no guest
		// boots unreachable.
		host.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("scratch-host listening on %s (guest on %s)", *listen, subnet)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-drained
}
