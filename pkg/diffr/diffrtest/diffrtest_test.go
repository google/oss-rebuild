// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffrtest

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func gz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Name = name // stored in the gzip header; diffr ignores it
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDiff(t *testing.T) {
	if diff := Diff(t, []byte("hello\nworld\n"), []byte("hello\nworld\n")); diff != "" {
		t.Errorf("equal inputs: want empty diff, got:\n%s", diff)
	}
	if diff := Diff(t, []byte("hello\nworld\n"), []byte("hello\nthere\n")); diff == "" {
		t.Error("differing inputs: want non-empty diff, got empty")
	}
	// Byte-different gzips with identical content: diffr considers these
	// equivalent, but the helper must still report a difference.
	a := gz(t, "a.txt", []byte("identical"))
	b := gz(t, "b.txt", []byte("identical"))
	if bytes.Equal(a, b) {
		t.Fatal("test setup: gzip bytes unexpectedly equal")
	}
	if diff := Diff(t, a, b); diff == "" {
		t.Error("byte-different gzips: want non-empty diff, got empty")
	}
}
