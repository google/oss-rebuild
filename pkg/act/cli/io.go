// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package cli provides utilities for building CLI commands using the act framework.
package cli

import "io"

// IO provides input/output streams for CLI commands.
type IO struct {
	In  io.Reader // stdin
	Out io.Writer // stdout
	Err io.Writer // stderr
}
