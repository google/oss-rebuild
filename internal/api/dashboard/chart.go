// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

// Chart is one time-series chart, drawn by charts.js's series form.
type Chart struct {
	Start, End int64
	Step       int64      // seconds apart, wider gaps break lines
	Fmt        string     // "num" or "pct"
	Threshold  *Threshold `json:",omitempty"`
	EndLabels  bool       `json:",omitempty"` // names each series past its last point
	Series     []ChartSeries
}

type Threshold struct {
	Value float64
	Above bool // holds above Value, not below
}

type ChartSeries struct {
	Label  string
	Class  string // color class: ok, bad, muted, group
	Kind   string // line, area or dash
	Points [][2]float64
}
