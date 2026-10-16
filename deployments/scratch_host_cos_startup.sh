#!/bin/bash
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Scratch host init: prepares the guest bridge and NAT, sizes guests to the
# host, and writes the marker the broker's unit waits for.
#
# Templated by Terraform with:
#   guest_subnet - host-only subnet the guest address comes from

set -euxo pipefail

# Golden images, overlays and exec records, on the stateful partition.
HOST_DIR=/var/lib/scratch-host
mkdir -p "$HOST_DIR"

# The broker owns the guest taps, this script the bridge. The guests' subnet
# is host-only and egresses through NAT.
BRIDGE=mvmbr0
GUEST_SUBNET='${guest_subnet}'
HOST_NIC="$(ip -o -4 route show default | awk '{print $5; exit}')"
MTU="$(cat "/sys/class/net/$HOST_NIC/mtu")"

# The guest tap runs with vhost=on. Best-effort, since QEMU falls back.
modprobe vhost_net || true

ip link show "$BRIDGE" >/dev/null 2>&1 || ip link add "$BRIDGE" type bridge
ip link set "$BRIDGE" mtu "$MTU"
ip link set "$BRIDGE" up
sysctl -w net.ipv4.ip_forward=1

NET="$${GUEST_SUBNET%%/*}"
ip addr replace "$${NET%.*}.1/$${GUEST_SUBNET##*/}" dev "$BRIDGE"
iptables -w -t nat -C POSTROUTING -s "$GUEST_SUBNET" -o "$HOST_NIC" -j MASQUERADE 2>/dev/null ||
    iptables -w -t nat -A POSTROUTING -s "$GUEST_SUBNET" -o "$HOST_NIC" -j MASQUERADE
iptables -w -C FORWARD -i "$BRIDGE" -j ACCEPT 2>/dev/null || iptables -w -I FORWARD 1 -i "$BRIDGE" -j ACCEPT
iptables -w -C FORWARD -o "$BRIDGE" -j ACCEPT 2>/dev/null || iptables -w -I FORWARD 1 -o "$BRIDGE" -j ACCEPT
# Guests need no GCP credentials. Refuse them the metadata server's HTTP API,
# which NAT would reach as the host. DNS on that address stays open.
# Inserted at the top, ahead of the accepts.
iptables -w -C FORWARD -i "$BRIDGE" -d 169.254.169.254 -p tcp --dport 80 -j REJECT 2>/dev/null ||
    iptables -w -I FORWARD 1 -i "$BRIDGE" -d 169.254.169.254 -p tcp --dport 80 -j REJECT
# Drop guest-to-guest traffic routed through the host, ahead of the accepts.
iptables -w -C FORWARD -i "$BRIDGE" -o "$BRIDGE" -j DROP 2>/dev/null ||
    iptables -w -I FORWARD 1 -i "$BRIDGE" -o "$BRIDGE" -j DROP
# COS drops inbound by default. Open only the broker's port, as the VPC rule does.
iptables -w -C INPUT -p tcp --dport 8080 -j ACCEPT 2>/dev/null || iptables -w -A INPUT -p tcp --dport 8080 -j ACCEPT
# Guests may not open connections to the host, even to its sshd and broker.
iptables -w -C INPUT -i "$BRIDGE" -m conntrack --ctstate NEW -j DROP 2>/dev/null ||
    iptables -w -I INPUT 1 -i "$BRIDGE" -m conntrack --ctstate NEW -j DROP

# 4-vCPU guests, two per four host vCPUs, as the scratch fleet's CPU traces
# support. Memory less a reserve is split evenly, never overcommitted.
HOST_RESERVE_MB=2048
GUEST_VCPU=4
SLOTS=$(( $(nproc) * 2 / GUEST_VCPU ))
GUEST_MEM_MB=$(( ($(awk '/^MemTotal:/ {print $2}' /proc/meminfo) / 1024 - HOST_RESERVE_MB) / SLOTS ))

# The broker's unit reads the guest shape and MTU from here. The MTU must
# match the bridge.
printf 'MTU=%s\nSLOTS=%s\nGUEST_VCPU=%s\nGUEST_MEM_MB=%s\n' "$MTU" "$SLOTS" "$GUEST_VCPU" "$GUEST_MEM_MB" > "$HOST_DIR/host.env"

# On tmpfs, so the marker is only ever true for the boot that wrote it.
touch /run/scratch-host-ready

# Retry the unit now rather than in 5s. --no-block avoids a systemd job
# deadlock from inside google-startup-scripts.service.
systemctl restart --no-block scratch-host.service || true
