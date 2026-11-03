#!/bin/sh
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Stages the golden guest artifacts onto host storage, then execs the
# broker. Overlays chain off these files, which would be slow through the
# container's overlayfs. Staging on every start keeps the host in step with
# the image.
set -eu
GOLDEN=/var/lib/scratch-host/golden
mkdir -p "$GOLDEN"
for f in vmlinuz initrd.img rootfs.raw; do
    cp -f "/golden/$f" "$GOLDEN/$f.new" && mv -f "$GOLDEN/$f.new" "$GOLDEN/$f"
done
# The scratch disk ships empty, since seeding it needs a docker daemon the
# image build lacks. Format under a temporary name so a failed mkfs leaves
# nothing behind.
if [ ! -s "$GOLDEN/scratch.raw" ]; then
    truncate -s 64G "$GOLDEN/scratch.raw.new"
    mkfs.ext4 -q -F -m 0 "$GOLDEN/scratch.raw.new"
    mv -f "$GOLDEN/scratch.raw.new" "$GOLDEN/scratch.raw"
fi
exec /scratch-host "$@"
