// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Guest is a live guest.
type Guest struct {
	ScratchID string    `json:"scratch_id"`
	IP        string    `json:"ip"`
	Since     time.Time `json:"since,omitzero"`
}

// GuestList answers RouteList, one entry per occupied slot.
type GuestList struct {
	Guests     []Guest   `json:"guests"`
	Slots      int       `json:"slots"`                // how many guests the host runs at once
	LastServed time.Time `json:"last_served,omitzero"` // last guest op, or else startup
}

// guestName is every guest's hostname: guests never see each other.
const guestName = "guest"

// guestTap and guestMAC name slot i's interface on either side. 52:54:00 is
// QEMU's MAC prefix.
func guestTap(i int) string { return "mvmtap" + strconv.Itoa(i) }
func guestMAC(i int) string { return fmt.Sprintf("52:54:00:12:34:%02x", i) }

// GuestNet places guests on the host-only bridge: the host takes the
// subnet's first address and routes for them, and slot i's guest takes
// IPs[i].
type GuestNet struct {
	Prefix  netip.Prefix
	Gateway netip.Addr
	IPs     []netip.Addr
}

// GuestNetFor derives the addresses for slots guests from subnet, refusing
// one too small.
func GuestNetFor(subnet netip.Prefix, slots int) (GuestNet, error) {
	if !subnet.IsValid() {
		return GuestNet{}, errors.New("guest subnet not configured")
	}
	p := subnet.Masked()
	n := GuestNet{Prefix: p, Gateway: p.Addr().Next()}
	for ip := n.Gateway; len(n.IPs) < slots; {
		if ip = ip.Next(); !p.Contains(ip) {
			return GuestNet{}, errors.Errorf("guest subnet %s has no room for a gateway and %d guests", subnet, slots)
		}
		n.IPs = append(n.IPs, ip)
	}
	return n, nil
}

// GuestConfig is the guest shape.
type GuestConfig struct {
	VCPU           int
	MemMB          int
	DNS            string // resolver written into the guest
	MTU            int    // NOTE: above the host NIC's, big packets blackhole
	Workdir        string // default cwd for execs
	MaxOutputBytes int64  // kills execs past it. Zero: no cap
}

// Layout locates the golden images and the guest's state on the host.
type Layout struct {
	GuestDir string // per-slot overlays and console logs
	ExecsDir string // per-session exec ledgers
	Kernel   string
	Initrd   string
	Rootfs   string // read-only image
	Scratch  string // read-only image
}

func (l Layout) slotDir(i int) string { return path.Join(l.GuestDir, strconv.Itoa(i)) }
func (l Layout) overlay(i int) string { return path.Join(l.slotDir(i), "root.qcow2") }
func (l Layout) scratch(i int) string { return path.Join(l.slotDir(i), "scratch.qcow2") }
func (l Layout) console(i int) string { return path.Join(l.slotDir(i), "console.log") }

func (l Layout) execDir(scratchID string) string { return path.Join(l.ExecsDir, scratchID) }

// kernelCmdline builds the guest's kernel command line. Per-guest settings
// travel there because the shared rootfs is read-only. The credential in it
// is safe in the guest's world-readable /proc/cmdline: the broker's public
// key, and a host key that only this boot uses.
func kernelCmdline(cfg GuestConfig, n GuestNet, slot int, cred GuestCredential) string {
	enc := base64.StdEncoding.EncodeToString
	return strings.Join([]string{
		// See https://docs.kernel.org/admin-guide/kernel-parameters.html.
		"root=/dev/vda rw rootfstype=ext4",
		// Reset at once on a panic.
		"console=ttyS0 reboot=t panic=-1",
		// Trust the CPU RNG so boot never waits on entropy. Register only the
		// console UART.
		"random.trust_cpu=on 8250.nr_uarts=1",
		// The guest has no PS/2 controller to probe.
		"i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd",
		// Keep the eth0 name microvm-net configures.
		"net.ifnames=0",
		fmt.Sprintf("mvm_ip=%s mvm_gw=%s mvm_prefix=%d", n.IPs[slot], n.Gateway, n.Prefix.Bits()),
		fmt.Sprintf("mvm_mtu=%d mvm_dns=%s mvm_name=%s", cfg.MTU, cfg.DNS, guestName),
		// base64, since a parameter cannot hold spaces.
		"mvm_ssh_authorized_key=" + enc(cred.AuthorizedKey),
		"mvm_ssh_host_key=" + enc(cred.HostKey),
	}, " ")
}

// avx512Flags is every AVX-512 flag QEMU knows. Dropping avx512f alone
// still advertises the rest.
var avx512Flags = []string{
	"avx512f", "avx512dq", "avx512ifma", "avx512pf", "avx512er", "avx512cd", "avx512bw", "avx512vl",
	"avx512vbmi", "avx512vbmi2", "avx512vnni", "avx512bitalg", "avx512-vpopcntdq",
	"avx512-4vnniw", "avx512-4fmaps", "avx512-vp2intersect", "avx512-fp16", "avx512-bf16",
}

// qemuArgv builds the QEMU invocation. QEMU stays in the foreground: the
// broker reads guest liveness from its child process.
func qemuArgv(bin string, cfg GuestConfig, l Layout, n GuestNet, slot int, cred GuestCredential) []string {
	return []string{
		bin,
		"-M", "microvm,acpi=on,rtc=on",
		"-enable-kvm",
		// NOTE: AVX-512 enlarges signal frames past some old binaries' thread
		// stacks (node < 12.4 on musl crashes).
		"-cpu", "host,-" + strings.Join(avx512Flags, ",-"),
		"-smp", strconv.Itoa(cfg.VCPU),
		"-m", strconv.Itoa(cfg.MemMB),
		"-nodefaults",
		"-no-user-config",
		// A guest reset exits QEMU, so the broker sees a panicked guest die.
		"-no-reboot",
		"-display", "none",
		"-kernel", l.Kernel,
		"-initrd", l.Initrd,
		"-append", kernelCmdline(cfg, n, slot, cred),
		"-drive", "id=root,file=" + l.overlay(slot) + ",format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=root",
		"-drive", "id=scratch,file=" + l.scratch(slot) + ",format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=scratch",
		"-netdev", "tap,id=net0,ifname=" + guestTap(slot) + ",script=no,downscript=no,vhost=on",
		"-device", "virtio-net-device,netdev=net0,mac=" + guestMAC(slot),
		"-device", "virtio-rng-device",
		"-serial", "file:" + l.console(slot),
	}
}
