// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scheduler holds the data model for the onboarding queue: which
// package versions are being worked on, in what order, and where each one
// stands.
//
// This package holds only pure types and functions. The commands that drive
// the queue live in tools/ctl/command/onboard.
package scheduler
