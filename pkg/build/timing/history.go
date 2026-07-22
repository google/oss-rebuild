// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timing

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// LayerTimes are the completion clocks of a build's appended history entries,
// oldest first.
type LayerTimes struct {
	Times  []time.Time
	layers Layers
}

// ParseHistory extracts this build's layer completion times from
// `docker history --human=false --format '{{json .}}'` output, taking the
// newest Appended rows by count.
// NOTE: History clocks are second-granularity, so durations derived from
// their deltas carry up to a second of error per boundary.
// NOTE: Non-JSON lines are skipped so the same parser serves raw command
// output and a step log interleaving set -x traces and inspect output.
func ParseHistory(out []byte, l Layers) (LayerTimes, error) {
	valid := l.Appended > 0 && l.Setup >= 0 && l.Source > l.Setup && l.Source < l.Appended
	if l.Deps >= 0 {
		valid = valid && l.Deps > l.Source && l.Deps < l.Appended
	}
	if !valid {
		return LayerTimes{}, errors.Errorf("invalid layer declaration: %+v", l)
	}
	var times []time.Time
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var row struct{ CreatedAt string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, row.CreatedAt)
		if err != nil {
			continue
		}
		times = append(times, t)
	}
	if len(times) < l.Appended {
		return LayerTimes{}, errors.Errorf("history has %d parseable entries, plan appended %d", len(times), l.Appended)
	}
	// History lists newest first: reverse this build's rows to oldest-first.
	lt := LayerTimes{
		Times:  make([]time.Time, l.Appended),
		layers: l,
	}
	for i, t := range times[:l.Appended] {
		lt.Times[l.Appended-1-i] = t
	}
	return lt, nil
}

// Phases derives the phase durations from layer completion deltas, anchoring
// Setup at the caller's backend-relative buildStart since history has no
// start entry. Setup absorbs the image pull and preamble, and Deps absorbs
// the timewarp startup that its layer runs before installing.
// NOTE: A completion time predating buildStart, or out of order, means a
// cached layer carrying its original timestamp: unmeasured and refused,
// never zero.
func (lt LayerTimes) Phases(buildStart time.Time) (setup, source, deps time.Duration, err error) {
	prev := buildStart
	for _, t := range lt.Times {
		if t.Before(prev) {
			return 0, 0, 0, errors.New("cached or disordered layer clocks")
		}
		prev = t
	}
	l := lt.layers
	setup = lt.Times[l.Setup].Sub(buildStart)
	source = lt.Times[l.Source].Sub(lt.Times[l.Source-1])
	if l.Deps >= 0 {
		deps = lt.Times[l.Deps].Sub(lt.Times[l.Deps-1])
	}
	return setup, source, deps, nil
}
