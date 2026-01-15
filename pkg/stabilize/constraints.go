// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import "github.com/google/oss-rebuild/pkg/archive"

// Constraint determines if a stabilizer applies in a given context.
type Constraint interface {
	Matches(ctx *StabilizationContext) bool
}

// Format matches when current archive format equals f.
type Format archive.Format

// Matches returns true if the current archive format matches.
func (c Format) Matches(ctx *StabilizationContext) bool {
	return ctx.Format() == archive.Format(c)
}

// Formats matches any of the given formats.
type Formats []archive.Format

// Matches returns true if the current archive format is one of the given formats.
func (c Formats) Matches(ctx *StabilizationContext) bool {
	f := ctx.Format()
	for _, cf := range c {
		if f == cf {
			return true
		}
	}
	return false
}

// Always matches unconditionally.
type Always struct{}

// Matches always returns true.
func (Always) Matches(*StabilizationContext) bool { return true }
