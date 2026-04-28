// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
)

func TestWithNestedArchive(t *testing.T) {
	ctx := NewContext(archive.TarFormat)
	if ctx.Depth() != 0 {
		t.Errorf("root Depth() = %d, want 0", ctx.Depth())
	}
	if ctx.Format() != archive.TarFormat {
		t.Errorf("root Format() = %v, want TarFormat", ctx.Format())
	}

	nested := ctx.WithNestedArchive(archive.TarGzFormat, "data.tar.gz")
	if nested.Depth() != 1 {
		t.Errorf("nested Depth() = %d, want 1", nested.Depth())
	}
	if nested.Format() != archive.TarGzFormat {
		t.Errorf("nested Format() = %v, want TarGzFormat", nested.Format())
	}

	// Verify original context is unchanged.
	if ctx.Depth() != 0 {
		t.Errorf("original Depth() changed to %d after nesting", ctx.Depth())
	}
	if ctx.Format() != archive.TarFormat {
		t.Errorf("original Format() changed to %v after nesting", ctx.Format())
	}

	// Double-nesting.
	deep := nested.WithNestedArchive(archive.GzipFormat, "inner.gz")
	if deep.Depth() != 2 {
		t.Errorf("deep Depth() = %d, want 2", deep.Depth())
	}
	if deep.Format() != archive.GzipFormat {
		t.Errorf("deep Format() = %v, want GzipFormat", deep.Format())
	}
}

func TestWithNestedArchiveDoesNotAlias(t *testing.T) {
	ctx := NewContext(archive.TarFormat)
	nested1 := ctx.WithNestedArchive(archive.TarGzFormat, "a.tar.gz")
	nested2 := ctx.WithNestedArchive(archive.ZipFormat, "b.zip")

	if nested1.Format() == nested2.Format() {
		t.Error("siblings should have different formats")
	}
	if nested1.Depth() != 1 || nested2.Depth() != 1 {
		t.Error("siblings should both have depth 1")
	}
}

func TestAtDepth(t *testing.T) {
	ctx := NewContext(archive.TarFormat)
	nested := ctx.WithNestedArchive(archive.TarGzFormat, "data.tar.gz")

	if !AtDepth(0).Matches(ctx) {
		t.Error("AtDepth(0) should match root context")
	}
	if AtDepth(1).Matches(ctx) {
		t.Error("AtDepth(1) should not match root context")
	}
	if AtDepth(0).Matches(nested) {
		t.Error("AtDepth(0) should not match nested context")
	}
	if !AtDepth(1).Matches(nested) {
		t.Error("AtDepth(1) should match nested context")
	}
}

func TestMinDepth(t *testing.T) {
	ctx := NewContext(archive.TarFormat)
	nested := ctx.WithNestedArchive(archive.TarGzFormat, "data.tar.gz")

	if !MinDepth(0).Matches(ctx) {
		t.Error("MinDepth(0) should match root context")
	}
	if MinDepth(1).Matches(ctx) {
		t.Error("MinDepth(1) should not match root context")
	}
	if !MinDepth(0).Matches(nested) {
		t.Error("MinDepth(0) should match nested context")
	}
	if !MinDepth(1).Matches(nested) {
		t.Error("MinDepth(1) should match nested context")
	}
}
