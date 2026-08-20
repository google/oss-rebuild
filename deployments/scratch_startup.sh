#!/bin/bash
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# COS scratch VM startup script executed on the host. The worker is docker-run
# by the cloud-init scratch-worker.service (see scratch_cloudinit.yaml), which
# waits for the marker this script writes as its last step.
#
# Templated by Terraform with:
#   worker_binary_uri: gs://... full URI to the scratch-worker binary

set -euxo pipefail

# Merge the gcr mirror into COS's shipped daemon.json.
python3 -c 'import json,sys; print(json.dumps(json.loads(sys.argv[1] or "{}") | {"registry-mirrors": ["https://mirror.gcr.io"]}))' \
    "$(cat /etc/docker/daemon.json 2>/dev/null)" > /etc/docker/daemon.json
systemctl restart docker

# Docker binds inherit source-mount flags and build containers execute
# files from staging under /home/builder, so drop COS's noexec on /home.
mkdir -p /home/builder
mount -o remount,bind,nosuid,nodev,exec /home

# Fetch the worker binary into /var/lib/toolbox, the writable path COS
# mounts exec. With no SA attached, the metadata token endpoint 404s and the
# fetch is attempted unauthenticated.
WORKER_BINARY_URI='${worker_binary_uri}'
WORKER_BINARY_URL="https://storage.googleapis.com/$${WORKER_BINARY_URI#gs://}"
CURL_AUTH=()
if curl -fsS -H "Metadata-Flavor: Google" \
       "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" 2>/dev/null \
       | python3 -c 'import json, sys; print(json.load(sys.stdin)["access_token"])' > /tmp/sa_token 2>/dev/null; then
  (printf "Authorization: Bearer "; cat /tmp/sa_token) > /tmp/sa_auth_header
  chmod 600 /tmp/sa_auth_header /tmp/sa_token
  CURL_AUTH=(-H "@/tmp/sa_auth_header")
fi
mkdir -p /var/lib/toolbox
curl -fsSL "$${CURL_AUTH[@]}" -o /var/lib/toolbox/scratch-worker "$WORKER_BINARY_URL"
chmod +x /var/lib/toolbox/scratch-worker
rm -f /tmp/sa_token /tmp/sa_auth_header

# On tmpfs, so the marker is only ever true for the boot that wrote it.
touch /run/scratch-worker-ready

# Nudge the unit to retry now rather than in 5s, permitting a failure if
# cloud-init hasn't yet written the unit. --no-block: waiting on another unit
# from inside google-startup-scripts.service invites a systemd job deadlock.
systemctl restart --no-block scratch-worker.service || true
