#!/bin/bash
# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0
[[ ! -f "hugo.toml" ]] && echo "Error: Run from docs/ directory (expected file ./hugo.toml)" && exit 1
CGO_ENABLED=1 go run -tags extended github.com/gohugoio/hugo@v0.152.2 server \
  --bind 0.0.0.0 \
  -p 4000 \
  --disableFastRender \
  --baseURL "https://$HOSTNAME"
