// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package billyx

import (
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
)

func TestDirSize(t *testing.T) {
	bfs := memfs.New()
	files := map[string]string{
		"root/a":       "12345",
		"root/sub/b":   "1234567890",
		"root/sub/c/d": "1",
		"outside":      "ignored",
	}
	for path, content := range files {
		if err := util.WriteFile(bfs, path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	got, err := DirSize(bfs, "root")
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if want := int64(16); got != want {
		t.Errorf("DirSize = %d; want %d", got, want)
	}
	if _, err := DirSize(bfs, "missing"); err == nil {
		t.Error("DirSize(missing) = nil error; want error")
	}
}
