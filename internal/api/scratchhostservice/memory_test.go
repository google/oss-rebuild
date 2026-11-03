// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchhostservice

import "testing"

func TestProcKB(t *testing.T) {
	meminfo := []byte("MemTotal:       12267524 kB\nMemFree:          301000 kB\nMemAvailable:    8102364 kB\n")
	for _, tc := range []struct {
		name string
		want int64
	}{
		{"MemTotal", 12267524},
		{"MemAvailable", 8102364},
	} {
		if got, err := procKB(meminfo, tc.name); err != nil || got != tc.want {
			t.Errorf("procKB(%s) = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
	// A prefix of another field's name must not match it.
	if _, err := procKB(meminfo, "Mem"); err == nil {
		t.Error("procKB(Mem) = nil error; want no such line")
	}
}
