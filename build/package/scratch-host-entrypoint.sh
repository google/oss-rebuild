#!/bin/sh
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Stages the in-image golden guest artifacts into the host directory,
# then execs the broker with the caller's flags. Backing files must live
# on host storage: guest qcow2 overlays chain off them, and reads through
# the container's overlayfs would be far slower. Staging is
# unconditional: the guest runs as a broker child, so it never outlives
# this container, and copying keeps the host in lockstep with the image
# across broker restarts that changed the tag.
set -eu
GOLDEN=/var/lib/scratch-host/golden
mkdir -p "$GOLDEN"
for f in vmlinuz initrd.img rootfs.raw; do
    cp -f "/golden/$f" "$GOLDEN/$f.new" && mv -f "$GOLDEN/$f.new" "$GOLDEN/$f"
done
# The scratch disk ships empty (seeding needs a docker daemon the image
# build lacks): each session pays its own base-image pulls and warms from
# there. Sparse, so the virtual size only bounds a guest's docker usage.
# Formatted under a temporary name so a failed mkfs cannot leave a
# half-written image behind for every later start to boot from.
if [ ! -s "$GOLDEN/scratch.raw" ]; then
    truncate -s 64G "$GOLDEN/scratch.raw.new"
    mkfs.ext4 -q -F -m 0 "$GOLDEN/scratch.raw.new"
    mv -f "$GOLDEN/scratch.raw.new" "$GOLDEN/scratch.raw"
fi
exec /scratch-host "$@"
