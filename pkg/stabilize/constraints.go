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

// AtDepth matches when the nesting depth equals n.
type AtDepth int

// Matches returns true if the current depth equals n.
func (c AtDepth) Matches(ctx *StabilizationContext) bool {
	return ctx.Depth() == int(c)
}

// MinDepth matches when the nesting depth is at least n.
type MinDepth int

// Matches returns true if the current depth is at least n.
func (c MinDepth) Matches(ctx *StabilizationContext) bool {
	return ctx.Depth() >= int(c)
}

// ArchivePath matches when the current archive level's path equals the given value.
type ArchivePath string

// Matches returns true if the innermost archive level's path matches.
func (c ArchivePath) Matches(ctx *StabilizationContext) bool {
	if len(ctx.Levels) == 0 {
		return false
	}
	return ctx.Levels[len(ctx.Levels)-1].Path == string(c)
}
