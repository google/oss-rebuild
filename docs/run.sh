#!/bin/bash
# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0
[[ ! -f "_config.yml" ]] && echo "Error: Run from docs/ directory (expected file ./_config.yml)" && exit 1
docker build \
  --build-arg REPO_NWO="google/oss-rebuild" \
  -t jekyll-docs-site \
  .
docker run --rm -it \
  -p 4000:4000 \
  -v $(pwd):/site \
  -v $(pwd)/../bin/_site:/_site \
  -e JEKYLL_GITHUB_TOKEN="$(gh auth token)" \
  jekyll-docs-site
