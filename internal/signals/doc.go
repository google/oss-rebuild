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
// This package holds the record types, the scoring, and the exports' storage.
// The jobs that produce and consume the exports live in
// tools/ctl/command/onboard.
package signals
