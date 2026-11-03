// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/internal/versionx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// causesShown bounds the failure causes listed on the package page.
const causesShown = 6

// PackageEvent is one rebuild attempt or agent session of a package.
type PackageEvent struct {
	Created time.Time
	Agent   bool // an agent session rather than a rebuild attempt
	Version string
	Variant string // tells apart a version's artifacts, see artifactVariant
	Tone    string // ok, bad, warn or live, as the views' Tone names it
	Outcome string
	Href    string // the attempt or session page
	Stage   string // where a failure happened: infer, build or compare for a rebuild, agent for a session
	Cause   string // a failure in brief, see failureCause and SessionView.Reason
	Message string // a failure as recorded
}

func rebuildEvent(rb rundex.Rebuild) PackageEvent {
	v := NewRebuildView(rb)
	e := PackageEvent{Created: rb.Created, Version: rb.Version, Variant: artifactVariant(rb.Artifact, rb.Version), Tone: v.Tone(), Outcome: v.Outcome(), Href: v.Href()}
	if e.Failed() {
		e.setFailure(failedStage(rb.Message), failureCause(rb.Message), rb.Message, string(rb.Status))
	}
	return e
}

func sessionEvent(s schema.AgentSession) PackageEvent {
	v := NewSessionView(s)
	e := PackageEvent{Created: s.Created, Agent: true, Version: s.Target.Version, Variant: artifactVariant(s.Target.Artifact, s.Target.Version), Tone: v.Tone(), Outcome: v.Outcome(), Href: "/session/" + s.ID}
	if e.Failed() {
		e.setFailure("agent", v.Reason(), s.Summary, s.StopReason)
	}
	return e
}

// setFailure records why the event failed. A failure without a message reads
// as its status, so it still groups with its kind.
func (e *PackageEvent) setFailure(stage, cause, msg, status string) {
	e.Stage, e.Cause, e.Message = stage, strings.TrimSpace(cause), msg
	if e.Cause == "" {
		e.Cause = strings.ToLower(status)
	}
}

// Failed reports whether the event finished without reproducing.
func (e PackageEvent) Failed() bool { return e.Tone == "bad" || e.Tone == "warn" }

// Headline is the end of Cause after its last ": ", where a message names
// the failure itself. A give-up's own words stay whole.
func (e PackageEvent) Headline() string {
	_, h := e.causeParts()
	return h
}

// Context is what Cause says before Headline.
func (e PackageEvent) Context() string {
	c, _ := e.causeParts()
	return c
}

func (e PackageEvent) causeParts() (context, headline string) {
	if i := strings.LastIndex(e.Cause, ": "); i >= 0 && e.Tone != "warn" {
		return e.Cause[:i], e.Cause[i+2:]
	}
	return "", e.Cause
}

// VersionState is the coarse status a version's attempts and agent sessions
// roll up to. Its values name the status color classes.
type VersionState string

const (
	StateVerified VersionState = "ok"
	StateFailed   VersionState = "bad"
	StateRunning  VersionState = "live"
)

// Label is the state in words.
func (s VersionState) Label() string {
	switch s {
	case StateVerified:
		return "reproduced"
	case StateFailed:
		return "not reproduced"
	}
	return "running"
}

// VersionStatus is one attempted version of a package with its rebuild
// attempts and agent sessions.
type VersionStatus struct {
	Version string
	Encoded rebuild.EncodedTarget
	State   VersionState
	Events  []PackageEvent // oldest first, never empty
	Failure *PackageEvent  // the latest failed event, nil when none failed
	Fixed   bool           // a later event reproduced, so Failure is history
}

// Last is when the version was last tried.
func (v VersionStatus) Last() time.Time { return v.Events[len(v.Events)-1].Created }

func byCreated(a, b PackageEvent) int { return a.Created.Compare(b.Created) }

// computeVersionStatuses groups a package's events by version, newest
// version first. untried counts the published versions without any.
// It is pure (no I/O) so it can be unit tested with canned data.
func computeVersionStatuses(eco rebuild.Ecosystem, pkg string, published []string, events []PackageEvent) (statuses []VersionStatus, untried int) {
	byVer := map[string][]PackageEvent{}
	for _, e := range events {
		byVer[e.Version] = append(byVer[e.Version], e)
	}
	for _, v := range published {
		if byVer[v] == nil {
			untried++
		}
	}
	for v, evs := range byVer {
		slices.SortStableFunc(evs, byCreated)
		vs := VersionStatus{
			Version: v,
			Encoded: packagePathEncoding.Encode(rebuild.Target{Ecosystem: eco, Package: pkg, Version: v}),
			State:   StateFailed,
			Events:  evs,
		}
		for i := range evs {
			switch {
			case evs[i].Tone == "ok":
				vs.State, vs.Fixed = StateVerified, vs.Failure != nil
			case evs[i].Tone == "live" && vs.State != StateVerified:
				vs.State = StateRunning
			case evs[i].Failed():
				vs.Failure, vs.Fixed = &evs[i], false
			}
		}
		statuses = append(statuses, vs)
	}
	slices.SortFunc(statuses, func(a, b VersionStatus) int {
		return cmp.Or(versionx.ApproxCompare(b.Version, a.Version), strings.Compare(b.Version, a.Version))
	})
	return statuses, untried
}

// PackageSummary is the figures above the version ledger.
type PackageSummary struct {
	Verified, Attempted            int           // versions
	Rebuilds, Sessions             int           // every rebuild attempt and agent session
	FailedRebuilds, FailedSessions int           // the failed ones
	StreakRebuilds, StreakSessions int           // failures since LastOK, or all of them when nothing reproduced
	LastOK                         *PackageEvent // the latest event that reproduced
	Since                          time.Time     // LastOK's time, or the first failure's when nothing reproduced
	Failure                        *PackageEvent // the latest failed event
}

// Attempts counts rebuild attempts and agent sessions together.
func (s PackageSummary) Attempts() int { return s.Rebuilds + s.Sessions }

// summarize rolls the version ledger up into its summary. Rebuilds and
// sessions count apart throughout.
func summarize(statuses []VersionStatus) PackageSummary {
	s := PackageSummary{Attempted: len(statuses)}
	var events []PackageEvent
	for _, v := range statuses {
		if v.State == StateVerified {
			s.Verified++
		}
		events = append(events, v.Events...)
	}
	slices.SortStableFunc(events, byCreated)
	for i, e := range events {
		tally := func(rebuilds, sessions *int) {
			if e.Agent {
				*sessions++
			} else {
				*rebuilds++
			}
		}
		tally(&s.Rebuilds, &s.Sessions)
		switch {
		case e.Tone == "ok":
			s.LastOK, s.Since = &events[i], e.Created
			s.StreakRebuilds, s.StreakSessions = 0, 0
		case e.Failed():
			tally(&s.FailedRebuilds, &s.FailedSessions)
			tally(&s.StreakRebuilds, &s.StreakSessions)
			s.Failure = &events[i]
			if s.Since.IsZero() {
				s.Since = e.Created
			}
		}
	}
	return s
}

// FailureCause is a stage and headline some of a package's failures share.
type FailureCause struct {
	Latest   PackageEvent // the newest failure with this headline
	N        int
	First    time.Time
	Versions string // the versions it covers, named when few
	Pct      int    // N as a share of the most common cause's, for its bar
}

// failureCauses groups a package's failures by stage and headline, the most
// common first, and keeps causesShown of them. The headline leaves out the
// context that tends to differ between the same failures, such as a URL or
// an operation id. A cause's failures are all of one kind, since only
// sessions fail at the agent stage.
func failureCauses(statuses []VersionStatus) (causes []FailureCause, more int) {
	type key struct{ stage, headline string }
	at := map[key]int{}
	var versions [][]string
	for _, v := range statuses {
		for _, e := range v.Events {
			if !e.Failed() {
				continue
			}
			k := key{e.Stage, e.Headline()}
			i, ok := at[k]
			if !ok {
				i, at[k] = len(causes), len(causes)
				causes = append(causes, FailureCause{Latest: e, First: e.Created})
				versions = append(versions, nil)
			}
			c := &causes[i]
			c.N++
			if e.Created.Before(c.First) {
				c.First = e.Created
			}
			if !e.Created.Before(c.Latest.Created) {
				c.Latest = e
			}
			if vs := versions[i]; len(vs) == 0 || vs[len(vs)-1] != v.Version {
				versions[i] = append(vs, v.Version)
			}
		}
	}
	for i, vs := range versions {
		switch {
		case len(vs) == len(statuses) && len(vs) > 1:
			causes[i].Versions = "all " + strconv.Itoa(len(vs)) + " versions"
		case len(vs) <= 3:
			causes[i].Versions = strings.Join(vs, ", ")
		default:
			causes[i].Versions = strconv.Itoa(len(vs)) + " versions"
		}
	}
	slices.SortStableFunc(causes, func(a, b FailureCause) int {
		return cmp.Or(cmp.Compare(b.N, a.N), b.Latest.Created.Compare(a.Latest.Created))
	})
	if len(causes) > causesShown {
		causes, more = causes[:causesShown], len(causes)-causesShown
	}
	for i := range causes {
		causes[i].Pct = 100 * causes[i].N / causes[0].N
	}
	return causes, more
}
