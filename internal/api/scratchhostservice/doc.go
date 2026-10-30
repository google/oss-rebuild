// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scratchhostservice is the scratch host broker. It boots a QEMU
// microVM guest per session and serves the exec API for it. Guest
// processes are the only record of occupancy, so a restarted broker is
// empty.
//
// TODO: Serve multiple guests per host.
// TODO: Reach into the guest over ssh on the host-only bridge.
// TODO: Serve the exec API.
package scratchhostservice
