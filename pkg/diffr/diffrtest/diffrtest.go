// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package diffrtest provides a cmp.Diff-style helper for comparing artifact
// bytes with diffr in tests.
package diffrtest

import (
	"bytes"
	"testing"

	"github.com/google/oss-rebuild/pkg/diffr"
	"github.com/pkg/errors"
)

// Diff compares file-like content and produces a human-readable diff.
//
// Insprired by the cmp.Diff interface but with better output for archives.
// The verdict is byte-exact: any byte difference yields a non-empty result,
// even where diffr's semantic comparison would normalize it away (e.g. gzip
// header fields).
func Diff(t *testing.T, want, got []byte) string {
	t.Helper()
	if bytes.Equal(want, got) {
		return ""
	}
	var buf bytes.Buffer
	err := diffr.Diff(t.Context(),
		diffr.File{Name: "want", Reader: bytes.NewReader(want)},
		diffr.File{Name: "got", Reader: bytes.NewReader(got)},
		diffr.Options{Output: &buf},
	)
	if err != nil && !errors.Is(err, diffr.ErrNoDiff) {
		t.Fatalf("diffr.Diff: %v", err)
	}
	return buf.String()
}
