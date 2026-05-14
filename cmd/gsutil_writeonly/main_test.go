// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFileRejectsLocalSourceSymlink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside-file")
	if err := os.WriteFile(outside, []byte("outside marker"), 0600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "artifact.tgz")
	if err := os.Symlink(outside, src); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(base, "uploaded")
	err := copyFile(context.Background(), nil, src, dest)
	if err == nil {
		t.Fatal("copyFile succeeded for symlink source, want error")
	}
	if !strings.Contains(err.Error(), "failed to open regular file") {
		t.Fatalf("copyFile error = %v, want regular file error", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after rejected copy: %v", statErr)
	}
}
