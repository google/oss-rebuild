// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sgir implements the sysgraph IR format.
//
// The IR format is a set of events for each action in the sysgraph that can be used to
// construct the sysgraph efficiently after the fact.
//
// On disk the events are stored in proto-delimited files with the <action_id>.pbdelim as the filename.
// Optionally the raw events are also stored in the same directory with the <action_id>_raw_events.pbdelim as the filename.
//
// The events are defined in sygraph/proto/sygraph/events.proto.
//
// The sysgraph IR format is intended to be an implementation detail of sysgraph generation and is not a stable API.
package sgir
