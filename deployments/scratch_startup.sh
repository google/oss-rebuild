#!/bin/bash
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Worker VM startup script.
#
# The worker VM has NO attached service account — it holds zero GCP
# credentials. This script can still fetch the scratch-worker binary
# from GCS because the bootstrap-tools bucket is public-readable on
# public deployments; the binary itself doesn't contain anything
# sensitive.
# TODO: For private deployments, attach a narrowly-scoped SA with
# storage.objects.get on the bootstrap-tools bucket, or expose the
# binary via a signed URL in instance metadata.
#
# Templated by Terraform with:
#   worker_binary_uri - gs://... full URI to the scratch-worker binary
#   caller_sa         - email of the SA that calls the worker (agent-api's)
#   audience          - expected ID-token audience for inbound requests

set -euxo pipefail

DEBIAN_FRONTEND=noninteractive apt-get update -y
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    docker.io \
    curl \
    ca-certificates
systemctl enable --now docker

# Mount the local SSD at /var/lib/docker for IOPS.
if [ -e /dev/nvme0n1 ]; then
    mkfs.ext4 -F /dev/nvme0n1
    mkdir -p /var/lib/docker
    mount /dev/nvme0n1 /var/lib/docker
    systemctl restart docker
fi

useradd --create-home --home-dir /home/builder --shell /bin/bash builder || true
mkdir -p /opt/builder
chown -R builder:builder /opt/builder /home/builder

# TODO: Support non-public bootstrap tools.
WORKER_BINARY_URI='${worker_binary_uri}'
curl -fsSL -o /opt/builder/scratch-worker \
    "https://storage.googleapis.com/$${WORKER_BINARY_URI#gs://}"
chmod +x /opt/builder/scratch-worker

cat >/etc/systemd/system/scratch-worker.service <<EOF
[Unit]
Description=Scratch worker
After=docker.service
Requires=docker.service

[Service]
Type=simple
User=builder
WorkingDirectory=/home/builder
ExecStart=/opt/builder/scratch-worker \\
    --caller-sa=${caller_sa} \\
    --audience=${audience} \\
    --docker-socket=/var/run/docker.sock \\
    --disk-paths=/var/lib/docker,/home/builder \\
    --listen=:8080
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now scratch-worker.service
