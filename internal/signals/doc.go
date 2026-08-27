// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package signals defines the priority signals that rank packages for
// onboarding: per-package measures of importance, each normalized within its
// ecosystem into (0,1] so heavy tails and incomparable registry scales stay
// out of the arithmetic, and combined into one priority score at read time.
//
// "Prevalence" is the signal derived from the dependency graphs of each
// ecosystem. We define it as "how many other packages depend on this one,
// directly or transitively," measured both per package and per version.
//
// The exports are also assembled into a published SQLite database, read by
// enqueue and by the snapshot. Unlike the snapshot's doc tables, its rows are
// plain columns with no raw document: the exports are the raw record and the
// database is regenerable from them, so per-row JSON would only triple the
// file (measured 183 vs 55 bytes per row at 10^6-package scale).
//
// This package holds the record types, the scoring, and that database. The
// jobs that produce and consume the exports live in tools/ctl/command/onboard.
package signals
