// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package marquee

import (
	"io"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFrames(t *testing.T) {
	tests := []struct {
		name     string
		frame    func(string, int) string
		art      string
		t        int
		expected string
	}{
		{name: "SpinTurnsCenterFirst", frame: spin, art: "↑↑↑↑↑↑↑↑↑", t: 1, expected: "↑↑↑↗↗↗↗↑↑"},
		{name: "RiseLiftsBubble", frame: rise, art: "#   #\n  0  \n#   #", t: 1, expected: "# 0 #\n     \n#   #"},
		{name: "RiseHidesBehindArt", frame: rise, art: "###\n 0 ", t: 1, expected: "###\n   "},
		{name: "RiseKeepsAttachedGlyphs", frame: rise, art: "x0 \n   ", t: 1, expected: "x0 \n   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.expected, tc.frame(tc.art, tc.t)); diff != "" {
				t.Errorf("unexpected frame (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFinalFrameIsArt(t *testing.T) {
	const art = "↑ 0 ↗\n  .  \n↓ x ←\n"
	tests := []struct {
		name  string
		frame func(string, int) string
		n     int
	}{
		{name: "Spin", frame: spin, n: spinFrames(art)},
		{name: "Rise", frame: rise, n: len(parse(art))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(art, tc.frame(art, tc.n)); diff != "" {
				t.Errorf("unexpected final frame (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPipeIsStatic(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	Spin(w, "↑")
	w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("↑\n", string(got)); diff != "" {
		t.Errorf("unexpected output (-want +got):\n%s", diff)
	}
}
