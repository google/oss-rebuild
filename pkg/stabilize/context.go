// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import "github.com/google/oss-rebuild/pkg/archive"

// StabilizationContext tracks position within potentially nested archives
// and carries the stabilizer pipeline for recursive application.
type StabilizationContext struct {
	Levels      []ArchiveLevel
	Entry       *EntryInfo
	Stabilizers []Stabilizer
}

// ArchiveLevel represents a single level in a nested archive.
type ArchiveLevel struct {
	// Format is the type of archive at this nesting level.
	Format archive.Format
	// Path is the path of this archive relative to the parent nesting level.
	Path string
}

// EntryInfo provides information about the current entry being stabilized.
type EntryInfo struct {
	// Path is the name of this entry within its enclosing archive.
	Path string
}

// NewContext creates a context for the outermost archive.
func NewContext(format archive.Format) *StabilizationContext {
	return &StabilizationContext{
		Levels: []ArchiveLevel{{Format: format, Path: ""}},
	}
}

// Depth returns the current nesting depth (0 for the outermost archive).
func (ctx *StabilizationContext) Depth() int {
	return len(ctx.Levels) - 1
}

// Format returns the format of the current archive layer.
func (ctx *StabilizationContext) Format() archive.Format {
	if len(ctx.Levels) == 0 {
		return archive.UnknownFormat
	}
	return ctx.Levels[len(ctx.Levels)-1].Format
}

// WithStabilizers returns a new context with the given stabilizers.
func (ctx *StabilizationContext) WithStabilizers(stabilizers []Stabilizer) *StabilizationContext {
	ctx.Stabilizers = stabilizers
	return ctx
}

// WithEntry returns a new context with the given entry information.
func (ctx *StabilizationContext) WithEntry(path string) *StabilizationContext {
	return &StabilizationContext{
		Levels:      ctx.Levels,
		Entry:       &EntryInfo{Path: path},
		Stabilizers: ctx.Stabilizers,
	}
}
