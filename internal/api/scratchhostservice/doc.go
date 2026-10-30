// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scratchhostservice is the scratch host broker. It boots a QEMU
// microVM guest per session, reaches it over ssh on a host-only bridge,
// and serves the exec API for it. Guest processes are the only record of
// occupancy, so a restarted broker is empty. Exec records outlive their
// guest until the session is deleted.
//
// TODO: Serve multiple guests per host.
package scratchhostservice
