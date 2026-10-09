// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[SessionRequest, SessionData, *Deps] = Session

type SessionRequest struct {
	ID string
}

func (SessionRequest) Validate() error { return nil }

// IterationGroup is one iteration's slice of the trajectory.
type IterationGroup struct {
	Number         int
	Record         *schema.AgentIteration
	RecordStrategy string
	RecordUsage    string
}

type SessionData struct {
	Session    SessionView
	Duration   string
	Usage      string
	Trajectory []IterationGroup
	Warnings   []string
}

// Session renders one agent session's trajectory: the session record joined
// with its iteration records.
func Session(ctx context.Context, req SessionRequest, deps *Deps) (*SessionData, error) {
	sessions, err := deps.Sessions.FetchSessions(ctx, &rundex.FetchSessionsReq{IDs: []string{req.ID}})
	if err != nil {
		return nil, errors.Wrap(err, "fetching session")
	}
	if len(sessions) == 0 {
		return nil, errors.Errorf("session %q not found", req.ID)
	}
	s := sessions[0]
	data := &SessionData{
		Session: NewSessionView(s),
		Usage:   usageString(s.Usage),
	}
	if !s.Updated.IsZero() && !s.Created.IsZero() {
		data.Duration = s.Updated.Sub(s.Created).Round(time.Second).String()
	}
	groups := make(map[int]*IterationGroup)
	groupFor := func(n int) *IterationGroup {
		if g, ok := groups[n]; ok {
			return g
		}
		g := &IterationGroup{Number: n}
		groups[n] = g
		return g
	}
	// Iterations are best-effort. A failure to load them shouldn't sink the
	// page.
	iters, err := deps.Sessions.FetchIterations(ctx, &rundex.FetchIterationsReq{SessionID: s.ID})
	if err != nil {
		data.Warnings = append(data.Warnings, fmt.Sprintf("loading iterations: %v", err))
	}
	for i := range iters {
		g := groupFor(iters[i].Number)
		g.Record = &iters[i]
		if iters[i].Strategy != nil {
			b, _ := json.MarshalIndent(iters[i].Strategy, "", "  ")
			g.RecordStrategy = string(b)
		}
		g.RecordUsage = usageString(iters[i].Usage)
	}
	for _, g := range groups {
		data.Trajectory = append(data.Trajectory, *g)
	}
	sort.Slice(data.Trajectory, func(i, j int) bool { return data.Trajectory[i].Number < data.Trajectory[j].Number })
	return data, nil
}

func usageString(u *schema.TokenUsage) string {
	if u == nil {
		return ""
	}
	cached := ""
	if u.Input > 0 && u.CachedInput > 0 {
		cached = fmt.Sprintf(" (%d%% cached)", int(float64(u.CachedInput)/float64(u.Input)*100+0.5))
	}
	return fmt.Sprintf("%s in%s / %s out", humanCount(u.Input), cached, humanCount(u.Output))
}
