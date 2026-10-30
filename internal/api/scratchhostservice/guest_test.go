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
		IP:      netip.MustParseAddr("10.200.0.2"),
	}
	for _, tc := range []struct {
		name    string
		subnet  netip.Prefix
		want    GuestNet
		wantErr bool
	}{
		{"HostFirstGuestNext", netip.MustParsePrefix("10.200.0.0/24"), want, false},
		{"UnmaskedInputIsMasked", netip.MustParsePrefix("10.200.0.9/24"), want, false},
		{"TooSmallForGatewayAndGuest", netip.MustParsePrefix("10.200.0.0/31"), GuestNet{}, true},
		{"Unset", netip.Prefix{}, GuestNet{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GuestNetFor(tc.subnet)
			if (err != nil) != tc.wantErr {
				t.Fatalf("GuestNetFor(%s) error = %v; wantErr %v", tc.subnet, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("GuestNetFor(%s) = %+v; want %+v", tc.subnet, got, tc.want)
			}
		})
	}
}

// TestQEMUArgv pins the invocation. The mvm_* parameters are the boot
// units' contract, and the drive order makes the scratch disk /dev/vdb.
func TestQEMUArgv(t *testing.T) {
	guestNet, err := GuestNetFor(netip.MustParsePrefix("10.200.0.0/24"))
	if err != nil {
		t.Fatalf("GuestNetFor: %v", err)
	}
	cfg := GuestConfig{VCPU: 4, MemMB: 8192, DNS: "169.254.169.254", MTU: 1460}
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
		"-cpu", "host",
		"-smp", "4",
		"-m", "8192",
		"-nodefaults",
		"-no-user-config",
		"-no-reboot",
		"-display", "none",
		"-kernel", "/golden/vmlinuz",
		"-initrd", "/golden/initrd.img",
		"-append", "root=/dev/vda rw rootfstype=ext4 console=ttyS0 reboot=t panic=-1 random.trust_cpu=on 8250.nr_uarts=1 i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd net.ifnames=0 mvm_ip=10.200.0.2 mvm_gw=10.200.0.1 mvm_prefix=24 mvm_mtu=1460 mvm_dns=169.254.169.254 mvm_name=guest",
		"-drive", "id=root,file=/var/lib/scratch-host/guest/root.qcow2,format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=root",
		"-drive", "id=scratch,file=/var/lib/scratch-host/guest/scratch.qcow2,format=qcow2,if=none,cache=none,aio=native",
		"-device", "virtio-blk-device,drive=scratch",
		"-netdev", "tap,id=net0,ifname=mvmtap0,script=no,downscript=no,vhost=on",
		"-device", "virtio-net-device,netdev=net0,mac=52:54:00:12:34:56",
		"-device", "virtio-rng-device",
		"-serial", "file:/var/lib/scratch-host/guest/console.log",
	}
	if diff := cmp.Diff(want, qemuArgv("qemu-system-x86_64", cfg, l, guestNet)); diff != "" {
		t.Errorf("qemuArgv mismatch (-want +got):\n%s", diff)
	}
}
