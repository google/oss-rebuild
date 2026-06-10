#!/bin/bash
# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Worker VM startup script.
#
# The worker process makes no GCP calls of its own; agent-api drives it
# over private-IP HTTP using an ID token. The bootstrap fetch of the
# worker binary is the only step that may need GCP credentials:
#   - On public deployments the VM needs no attached service account as
#     the bootstrap-tools bucket is public-readable.
#   - On private deployments a narrowly-scoped SA with objectViewer on
#     the bootstrap-tools bucket is attached. We detect the SA presence
#     via the metadata server and authenticate the storage fetch with its
#     access token.
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
    jq \
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

WORKER_BINARY_URI='${worker_binary_uri}'
WORKER_BINARY_URL="https://storage.googleapis.com/$${WORKER_BINARY_URI#gs://}"

# If an SA is bound to this VM (private deployments), mint an access
# token from the metadata server and use it to authenticate the
# bootstrap fetch. On public deployments no SA is attached, the
# metadata endpoint 404s, and the fetch is anonymous.
CURL_AUTH=()
if curl -fsS -H "Metadata-Flavor: Google" \
       "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" 2>/dev/null \
       | jq -re .access_token > /tmp/sa_token 2>/dev/null && [ -s /tmp/sa_token ]; then
  (printf "Authorization: Bearer "; cat /tmp/sa_token) > /tmp/sa_auth_header
  chmod 600 /tmp/sa_auth_header /tmp/sa_token
  CURL_AUTH=(-H "@/tmp/sa_auth_header")
fi

curl -fsSL "$${CURL_AUTH[@]}" -o /opt/builder/scratch-worker "$WORKER_BINARY_URL"
chmod +x /opt/builder/scratch-worker
rm -f /tmp/sa_token /tmp/sa_auth_header

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
