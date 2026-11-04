// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package difftool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDiffoscope(t *testing.T) {
	if _, err := exec.LookPath("diffoscope"); err != nil {
		t.Skip("diffoscope not installed, skipping test")
	}

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	fileC := filepath.Join(dir, "c.txt")

	if err := os.WriteFile(fileA, []byte("common line\nline from A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("common line\nline from B\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileC, []byte("common line\nline from A\n"), 0644); err != nil {
		t.Fatal(err)
	}

	target := rebuild.Target{
		Ecosystem: rebuild.Debian,
		Package:   "test-pkg",
		Version:   "1.0",
		Artifact:  "test-pkg.deb",
	}

	d := Diffoscope{}

	t.Run("identical files", func(t *testing.T) {
		out, err := d.Diff(context.Background(), fileA, fileC, target)
		if err != nil {
			t.Fatalf("unexpected error on identical files: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("expected empty output on identical files, got: %s", string(out))
		}
	})

	t.Run("different files", func(t *testing.T) {
		out, err := d.Diff(context.Background(), fileA, fileB, target)
		if err != nil {
			t.Fatalf("unexpected error on differing files: %v", err)
		}
		if !strings.Contains(string(out), "line from A") || !strings.Contains(string(out), "line from B") {
			t.Errorf("expected diff to contain differences, got: %s", string(out))
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := d.Diff(ctx, fileA, fileB, target)
		if err == nil {
			t.Error("expected error on canceled context, got nil")
		}
	})
}
