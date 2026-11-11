// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package marquee animates the ASCII art logos of the CLI tools.
package marquee

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"
)

// arrows is ordered clockwise, so each entry is a 45 degree turn from the last.
var arrows = []rune("↑↗→↘↓↙←↖")

// Spin prints art to f and turns each arrow once as a ring spreads from the center.
func Spin(f *os.File, art string) {
	play(f, art, spinFrames(art), 50*time.Millisecond, spin)
}

// Rise prints art to f and carries its lone "0" and "." glyphs once around its rows.
func Rise(f *os.File, art string) {
	play(f, art, len(parse(art)), 80*time.Millisecond, rise)
}

// play prints art and redraws frames 1 through n over it using ESC[nA, which
// moves the cursor up n rows. Art stays static on a pipe, a dumb terminal, or one too small to hold it.
func play(f *os.File, art string, n int, tick time.Duration, frame func(string, int) string) {
	fmt.Fprintln(f, art)
	g := parse(art)
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || os.Getenv("TERM") == "dumb" || w < width(g) || h <= len(g) {
		return
	}
	for t := 1; t <= n; t++ {
		time.Sleep(tick)
		fmt.Fprintf(f, "\x1b[%dA%s\n", len(g), frame(art, t))
	}
}

func spin(art string, t int) string {
	g := parse(art)
	w := width(g)
	for y, l := range g {
		for x, r := range l {
			if i, s := slices.Index(arrows, r), t-distance(x, y, w, len(g)); i >= 0 && 0 < s && s < len(arrows) {
				l[x] = arrows[(i+s)%len(arrows)]
			}
		}
	}
	return render(g)
}

// spinFrames is the frame by which the ring has passed the farthest corner.
func spinFrames(art string) int {
	g := parse(art)
	return distance(0, 0, width(g), len(g)) + len(arrows)
}

// distance is how far a cell is from the center of a w by h grid.
// Terminal cells are about twice as tall as they are wide.
func distance(x, y, w, h int) int {
	return int(math.Hypot(float64(2*x-w)/4, float64(2*y-h)/2))
}

// rise lifts each bubble t rows, wrapping at the top and hiding behind other glyphs.
func rise(art string, t int) string {
	type bubble struct {
		x, y int
		r    rune
	}
	g, out := parse(art), parse(art)
	var bubbles []bubble
	for y, l := range g {
		for x, r := range l {
			if (r == '0' || r == '.') && x > 0 && x+1 < len(l) && l[x-1] == ' ' && l[x+1] == ' ' {
				bubbles = append(bubbles, bubble{x, y, r})
				out[y][x] = ' '
			}
		}
	}
	for _, b := range bubbles {
		y := (b.y - t + len(g)) % len(g)
		if b.x < len(out[y]) && out[y][b.x] == ' ' {
			out[y][b.x] = b.r
		}
	}
	return render(out)
}

func parse(art string) [][]rune {
	var g [][]rune
	for l := range strings.SplitSeq(art, "\n") {
		g = append(g, []rune(l))
	}
	return g
}

func render(g [][]rune) string {
	lines := make([]string, len(g))
	for y, l := range g {
		lines[y] = string(l)
	}
	return strings.Join(lines, "\n")
}

func width(g [][]rune) int {
	return len(slices.MaxFunc(g, func(a, b []rune) int { return cmp.Compare(len(a), len(b)) }))
}
