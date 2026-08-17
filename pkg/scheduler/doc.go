// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scheduler holds the data model for the onboarding queue: which
// package versions are being worked on, in what order, and where each one
// stands.
//
// "Campaign" tracks one package version's progress through the escalation
// ladder of stages, each more expensive than the last. The queue is ordered by
// DispatchOrder, which combines the priority score with recency so that
// importance and freshness both count. Tick advances a campaign given the
// outcome of its last attempt. Only the agent stage is rationed: build
// throughput is ample, agent compute is not.
//
// This package holds only pure types and functions. The commands that drive
// the queue live in tools/ctl/command/onboard.
package scheduler
