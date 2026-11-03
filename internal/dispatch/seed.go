// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"cmp"
	"context"
	"path"
	"slices"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/versionx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// seedFor is the confirmed build of t's nearest reproduced sibling, if any.
func (d *Dispatcher) seedFor(ctx context.Context, t rebuild.Target) (*schema.AgentSeed, error) {
	sessions, err := d.Sessions.ListPackage(ctx, t.Ecosystem, t.Package)
	if err != nil {
		return nil, errors.Wrap(err, "listing the package's sessions")
	}
	s := nearestSibling(t, sessions)
	if s == nil {
		return nil, nil
	}
	it, err := d.Iterations.Get(ctx, db.IterationKey{SessionID: s.ID, ID: s.SuccessIteration})
	if err != nil {
		return nil, errors.Wrapf(err, "reading the confirmed build of session %s", s.ID)
	}
	return &schema.AgentSeed{Version: s.Target.Version, Session: s.ID, Strategy: it.Strategy}, nil
}

// nearestSibling is the session that reproduced the version of t's package
// nearest t with the same artifact kind, the latest on a tie.
func nearestSibling(t rebuild.Target, sessions []schema.AgentSession) *schema.AgentSession {
	var cands []*schema.AgentSession
	versions := []string{t.Version}
	for i, s := range sessions {
		switch {
		case s.Status != schema.AgentSessionStatusCompleted,
			s.StopReason != schema.AgentCompleteReasonSuccess,
			s.SuccessIteration == "",
			s.Target.Version == t.Version,
			path.Ext(s.Target.Artifact) != path.Ext(t.Artifact):
			continue
		}
		cands = append(cands, &sessions[i])
		versions = append(versions, s.Target.Version)
	}
	if len(cands) == 0 {
		return nil
	}
	slices.Sort(versions)
	versions = slices.Compact(versions)
	slices.SortStableFunc(versions, versionx.ApproxCompare)
	pos := make(map[string]int, len(versions))
	for i, v := range versions {
		pos[v] = i
	}
	dist := func(s *schema.AgentSession) int {
		d := pos[s.Target.Version] - pos[t.Version]
		return max(d, -d)
	}
	return slices.MinFunc(cands, func(a, b *schema.AgentSession) int {
		return cmp.Or(cmp.Compare(dist(a), dist(b)), b.Updated.Compare(a.Updated))
	})
}
