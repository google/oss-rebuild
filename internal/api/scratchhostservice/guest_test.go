// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestGuestNetFor(t *testing.T) {
	want := GuestNet{
		Prefix:  netip.MustParsePrefix("10.200.0.0/24"),
		Gateway: netip.MustParseAddr("10.200.0.1"),
		IPs:     []netip.Addr{netip.MustParseAddr("10.200.0.2"), netip.MustParseAddr("10.200.0.3")},
	}
	for _, tc := range []struct {
		name    string
		subnet  netip.Prefix
		slots   int
		want    GuestNet
		wantErr bool
	}{
		{"HostFirstGuestsNext", netip.MustParsePrefix("10.200.0.0/24"), 2, want, false},
		{"UnmaskedInputIsMasked", netip.MustParsePrefix("10.200.0.9/24"), 2, want, false},
		{"TooSmallForGatewayAndGuest", netip.MustParsePrefix("10.200.0.0/31"), 1, GuestNet{}, true},
		{"TooSmallForEverySlot", netip.MustParsePrefix("10.200.0.0/30"), 3, GuestNet{}, true},
		{"Unset", netip.Prefix{}, 1, GuestNet{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GuestNetFor(tc.subnet, tc.slots)
			if (err != nil) != tc.wantErr {
				t.Fatalf("GuestNetFor(%s, %d) error = %v; wantErr %v", tc.subnet, tc.slots, err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b netip.Addr) bool { return a == b }), cmp.Comparer(func(a, b netip.Prefix) bool { return a == b })); diff != "" {
				t.Errorf("GuestNetFor(%s, %d) mismatch (-want +got):\n%s", tc.subnet, tc.slots, diff)
			}
		})
	}
}

// TestQEMUArgv pins the invocation. The mvm_* parameters are the boot
// units' contract, and the drive order makes the scratch disk /dev/vdb.
func TestQEMUArgv(t *testing.T) {
	guestNet, err := GuestNetFor(netip.MustParsePrefix("10.200.0.0/24"), 2)
	if err != nil {
		t.Fatalf("GuestNetFor: %v", err)
	}
	cfg := GuestConfig{VCPU: 4, MemMB: 8192, DNS: "169.254.169.254", MTU: 1460}
	// Slot 1, so every per-slot name is checked against its index.
	l := Layout{
		GuestDir: "/var/lib/scratch-host/guest",
		Kernel:   "/golden/vmlinuz",
		Initrd:   "/golden/initrd.img",
		Rootfs:   "/golden/rootfs.raw",
		Scratch:  "/golden/scratch.raw",
	}
	want := []string{
		"qemu-system-x86_64",
		"-M", "microvm,acpi=on,rtc=on",
		"-enable-kvm",
		"-cpu", "host,-avx512f,-avx512dq,-avx512ifma,-avx512pf,-avx512er,-avx512cd,-avx512bw,-avx512vl,-avx512vbmi,-avx512vbmi2,-avx512vnni,-avx512bitalg,-avx512-vpopcntdq,-avx512-4vnniw,-avx512-4fmaps,-avx512-vp2intersect,-avx512-fp16,-avx512-bf16",
		"-smp", "4",
		"-m", "8192",
		"-nodefaults",
		"-no-user-config",
		"-no-reboot",
		"-display", "none",
		"-kernel", "/golden/vmlinuz",
		"-initrd", "/golden/initrd.img",
		"-append", "root=/dev/vda rw rootfstype=ext4 console=ttyS0 reboot=t panic=-1 random.trust_cpu=on 8250.nr_uarts=1 i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd net.ifnames=0 mvm_ip=10.200.0.3 mvm_gw=10.200.0.1 mvm_prefix=24 mvm_mtu=1460 mvm_dns=169.254.169.254 mvm_name=guest mvm_ssh_authorized_key=c3NoLWVkMjU1MTkgQUFBQQ== mvm_ssh_host_key=a2V5",
		"-drive", "id=root,file=/var/lib/scratch-host/guest/1/root.qcow2,format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=root",
		"-drive", "id=scratch,file=/var/lib/scratch-host/guest/1/scratch.qcow2,format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=scratch",
		"-netdev", "tap,id=net0,ifname=mvmtap1,script=no,downscript=no,vhost=on",
		"-device", "virtio-net-device,netdev=net0,mac=52:54:00:12:34:01",
		"-device", "virtio-rng-device",
		"-serial", "file:/var/lib/scratch-host/guest/1/console.log",
	}
	if diff := cmp.Diff(want, qemuArgv("qemu-system-x86_64", cfg, l, guestNet, 1, GuestCredential{AuthorizedKey: []byte("ssh-ed25519 AAAA"), HostKey: []byte("key")})); diff != "" {
		t.Errorf("qemuArgv mismatch (-want +got):\n%s", diff)
	}
}
