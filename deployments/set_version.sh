#!/bin/bash
# Copyright 2025 Google LLC
# SPDX-License-Identifier: Apache-2.0

# Version vars updater
#
# Updates or initializes version and commit information in a .tfvars file using
# Git/Jujutsu revision data.
#
# Usage:
#   ./script.sh <tfvars-file> <service-rev> [<prebuild-rev>]
#
# Requirements:
#   - Run from VCS root (.git or .jj)
#   - Requires: sed, grep, tee, sponge (moreutils)

tfvars=$1
svcrev=$2
prerev=$3
if [ "$tfvars" == "" ]; then
  echo "USAGE: $0 <tfvars-file> <service-rev> [<prebuild-rev>]" 1>&2
  exit 1
fi
if [ "$svcrev" == "" ]; then
  echo "USAGE: $0 <tfvars-file> <service-rev> [<prebuild-rev>]" 1>&2
  exit 1
fi
function set_or_update() {
  key=$1
  val=$2
  echo Setting $key to $val 1>&2
  tee >(grep -q "$key\s*=" || printf "$key = \"$val\"\n") | sed -E "s/($key\s*=\s*).*/\1\"$val\"/g"
}
function vcs_version() {
  rev=$1
  if [ -e .jj ]; then
    jj log -r "$rev" --color=never --no-graph -T '"v0.0.0-" ++ author.timestamp().utc().format("%Y%m%d%H%M%S") ++ "-" ++ commit_id.short()'
  elif [ -e .git ]; then
    TZ=UTC git show --quiet --abbrev=12 --date='format-local:%Y%m%d%H%M%S' --format="v0.0.0-%cd-%h" "$rev"
  else
    echo "Must be run from VCS root" 1>&2
    exit 1
  fi
}
function vcs_commit() {
  rev=$1
  if [ -e .jj ]; then
    jj log -r "$rev" --color=never --no-graph -T 'commit_id'
  elif [ -e .git ]; then
    git rev-parse "$rev"
  else
    echo "Must be run from VCS root" 1>&2
    exit 1
  fi
}

set -o pipefail
cat $tfvars | \
  set_or_update "service_version" $(vcs_version $svcrev) | \
  set_or_update "service_commit" $(vcs_commit $svcrev) | \
  if [ "$prerev" != "" ]; then \
    set_or_update "prebuild_version" $(vcs_version $prerev) | \
    set_or_update "prebuild_commit" $(vcs_commit $prerev)
  else cat; fi | \
  sponge $tfvars
