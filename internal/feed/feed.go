// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package feed admits new releases of tracked packages into the onboarding
// queue. A package is tracked while the published signal database ranks it.
// A release takes its package's score since a new version has no dependents.
package feed

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Event is one published release.
type Event struct {
	Ecosystem rebuild.Ecosystem
	Package   string
	Version   string
	Artifact  string    // resolved at admission when empty
	Published time.Time // the pass's own time when unknown
}

// Config shapes one pass.
type Config struct {
	Settle time.Duration // dispatch delay for registry propagation
}

// Defaults is the configuration a pass runs with when nothing is specified.
var Defaults = Config{Settle: 15 * time.Minute}

// Request is the HTTP form of a batch.
type Request struct {
	Events []Event `form:"events"`
	Settle string  `form:"settle"` // Go duration string, Defaults.Settle when empty
}

// Validate implements act.Input.
func (r Request) Validate() error {
	_, err := r.Config()
	return err
}

// Config resolves the request against Defaults.
func (r Request) Config() (Config, error) {
	c := Defaults
	if r.Settle != "" {
		d, err := time.ParseDuration(r.Settle)
		if err != nil {
			return c, errors.Wrap(err, "parsing settle")
		}
		c.Settle = d
	}
	return c, nil
}

// Summary counts one pass's events by disposition.
type Summary struct {
	Events     int
	Prerelease int // dropped: not a stable release
	Superseded int // dropped: a newer one is in the batch
	Untracked  int // dropped: no signal database rank
	Unresolved int // dropped: no artifact could be named
	Admitted   int
	Tracked    int // a campaign already existed
}

func (s Summary) String() string {
	return fmt.Sprintf("feed: events=%d admitted=%d tracked=%d untracked=%d prerelease=%d superseded=%d unresolved=%d",
		s.Events, s.Admitted, s.Tracked, s.Untracked, s.Prerelease, s.Superseded, s.Unresolved)
}

// Pass admits one batch of events. Err receives one line per dropped event.
type Pass struct {
	Campaigns db.Campaigns
	Signals   *sqlite3.Conn
	Registry  rebuild.RegistryMux
	Config    Config
	Err       io.Writer
	Now       time.Time
}

// Run admits the newest stable release of each tracked package in events.
// Insert refuses existing campaigns, so a replayed batch has no effect.
func (p *Pass) Run(ctx context.Context, events []Event) (*Summary, error) {
	s := &Summary{Events: len(events)}
	for _, e := range p.newest(events, s) {
		sig, ok, err := signals.PackageSignalAt(p.Signals, string(e.Ecosystem), e.Package)
		if err != nil {
			return nil, errors.Wrapf(err, "reading signals of %s", e.Package)
		}
		if !ok {
			s.Untracked++
			continue
		}
		t := rebuild.Target{Ecosystem: e.Ecosystem, Package: e.Package, Version: e.Version, Artifact: e.Artifact}
		if t.Artifact, err = meta.GuessArtifact(ctx, t, p.Registry); err != nil {
			s.Unresolved++
			fmt.Fprintf(p.Err, "skip %s@%s: resolving artifact: %v\n", e.Package, e.Version, err)
			continue
		}
		published := e.Published
		if published.IsZero() {
			published = p.Now
		}
		c := scheduler.Campaign{
			Ecosystem: string(t.Ecosystem), Package: t.Package, Version: t.Version, Artifact: t.Artifact,
			Stage:     scheduler.StageInfer,
			State:     scheduler.StateQueued,
			Score:     sig.Score,
			Published: published,
			Origin:    scheduler.OriginFeed,
			NotBefore: published.Add(p.Config.Settle),
			Updated:   p.Now,
		}
		switch err := p.Campaigns.Insert(ctx, c); err {
		case nil:
			s.Admitted++
		case db.ErrAlreadyExists:
			s.Tracked++
		default:
			fmt.Fprintf(p.Err, "admit %s@%s: %v\n", e.Package, e.Version, err)
		}
	}
	return s, nil
}

// newest keeps each package's latest-published stable release, so a burst of
// releases costs one campaign. Ties keep the later event.
func (p *Pass) newest(events []Event, s *Summary) []Event {
	latest := map[rebuild.Target]int{}
	var out []Event
	for _, e := range events {
		if prerelease(e.Ecosystem, e.Version) {
			s.Prerelease++
			fmt.Fprintf(p.Err, "skip %s@%s: prerelease\n", e.Package, e.Version)
			continue
		}
		key := rebuild.Target{Ecosystem: e.Ecosystem, Package: e.Package}
		if i, ok := latest[key]; ok {
			s.Superseded++
			if !e.Published.Before(out[i].Published) {
				out[i] = e
			}
			continue
		}
		latest[key] = len(out)
		out = append(out, e)
	}
	return out
}

// pep440Pre matches the pre-release and dev suffixes PEP 440 orders before the
// release they qualify, in any of the spellings it normalizes.
var pep440Pre = regexp.MustCompile(`(?i)(\d[._-]?(a|b|c|rc|alpha|beta|pre|preview)[._-]?\d*|[\d._-]dev\d*)($|[._+-])`)

// prerelease reports whether version marks a pre-release in its ecosystem's
// scheme. A version semver cannot parse counts as one.
// TODO: Move this definition to rebuild so other policy can use it.
func prerelease(eco rebuild.Ecosystem, version string) bool {
	switch eco {
	case rebuild.NPM, rebuild.CratesIO:
		v, err := semver.New(version)
		return err != nil || v.Prerelease != ""
	case rebuild.PyPI:
		return pep440Pre.MatchString(version)
	case rebuild.RubyGems:
		return regexp.MustCompile(`[a-zA-Z]`).MatchString(version) // any letter marks a prerelease
	}
	return false
}
