// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/genai"
)

var _ api.HandlerFn[SessionRequest, SessionData, *Deps] = Session

type SessionRequest struct {
	ID string
}

func (SessionRequest) Validate() error { return nil }

// TranscriptEvent is one rendered entry of the session's trajectory: a
// prompt, model prose or thought, a tool call, a tool response, or the
// first-iteration inference proposal.
type TranscriptEvent struct {
	Kind    string // prompt | model | thought | call | response | proposal
	Title   string
	At      time.Time // zero for the proposal, which anchors its iteration
	Command string    // call only: the command argument, shown verbatim
	Body    string
	Note    string // summary line when the body renders collapsed
	Open    bool   // render the body expanded rather than behind a toggle
}

// IterationGroup is one iteration's slice of the trajectory: the transcript
// events under messages/<n>/ plus the iteration record when one exists. In
// scratch mode only the GCB confirmation is recorded server-side, so groups
// without a record are the norm there.
type IterationGroup struct {
	Number         int
	Events         []TranscriptEvent
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
// with its iteration records and the chat transcript dump.
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
	// Iterations and the transcript are best-effort. A failure to load
	// either shouldn't sink the page.
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
	if deps.GCSClient != nil && deps.SessionsBucket != "" {
		if err := loadTranscript(ctx, deps, s.ID, groupFor); err != nil {
			data.Warnings = append(data.Warnings, fmt.Sprintf("loading transcript: %v", err))
		}
	} else {
		data.Warnings = append(data.Warnings, "transcripts not configured: pass -sessions-bucket")
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

// maxTranscriptObjectBytes bounds each transcript object read. Objects
// embed tool output tails capped at ~100KB by the agent, so hitting this
// means something unexpected.
// NOTE: The event renders truncated rather than failing the page.
const maxTranscriptObjectBytes = 2 << 20

// transcriptReadConcurrency bounds the object reads in flight for a page load.
// A session's transcript is a few hundred small objects, so the load is round
// trips rather than bytes.
const transcriptReadConcurrency = 16

// transcriptName is the parsed form of one transcript object name,
// <sessionID>/messages/<iter>/<invokeTime>-<part>-<role>.json or the fixed
// <sessionID>/messages/<iter>/0-inference-proposal.json.
type transcriptName struct {
	Iter     int
	Time     time.Time
	Part     int
	Role     string
	Proposal bool
}

// transcriptNameRE matches a transcript object name relative to "messages/".
var transcriptNameRE = regexp.MustCompile(`^(\d+)/(?:0-inference-proposal|(.+)-(\d+)-([a-z]+))\.json$`)

// parseTranscriptName parses a transcript object name, reporting ok=false
// for objects under the prefix that are not transcript messages.
func parseTranscriptName(name string) (transcriptName, bool) {
	m := transcriptNameRE.FindStringSubmatch(name)
	if m == nil {
		return transcriptName{}, false
	}
	iter, _ := strconv.Atoi(m[1])
	if m[2] == "" {
		return transcriptName{Iter: iter, Proposal: true}, true
	}
	// RFC3339Nano strips trailing zeros, so lexicographic order of the
	// prefix is not chronological. Callers must sort on the parsed time.
	when, err := time.Parse(time.RFC3339Nano, m[2])
	if err != nil {
		return transcriptName{}, false
	}
	part, _ := strconv.Atoi(m[3])
	return transcriptName{Iter: iter, Time: when, Part: part, Role: m[4]}, true
}

// loadTranscript lists the session's chat dump and appends the decoded
// events to their iteration groups in chronological order.
func loadTranscript(ctx context.Context, deps *Deps, sessionID string, groupFor func(int) *IterationGroup) error {
	prefix := sessionID + "/messages/"
	it := deps.GCSClient.Bucket(deps.SessionsBucket).Objects(ctx, &storage.Query{Prefix: prefix})
	type entry struct {
		name transcriptName
		obj  string
		body []byte
		ok   bool
	}
	var entries []entry
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return errors.Wrap(err, "listing transcript objects")
		}
		name, ok := parseTranscriptName(strings.TrimPrefix(attrs.Name, prefix))
		if !ok {
			continue
		}
		entries = append(entries, entry{name: name, obj: attrs.Name})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].name, entries[j].name
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		return a.Part < b.Part
	})
	var reads errgroup.Group
	reads.SetLimit(transcriptReadConcurrency)
	for i := range entries {
		reads.Go(func() error {
			e := &entries[i]
			body, err := readCapped(ctx, deps, e.obj)
			if err != nil {
				// One unreadable object shouldn't hide the rest of the dump.
				log.Printf("session %s: reading transcript object %s: %v", sessionID, e.obj, err)
				return nil
			}
			e.body, e.ok = body, true
			return nil
		})
	}
	reads.Wait()
	for _, e := range entries {
		if !e.ok {
			continue
		}
		g := groupFor(e.name.Iter)
		g.Events = append(g.Events, eventsFrom(e.name, e.body)...)
	}
	return nil
}

func readCapped(ctx context.Context, deps *Deps, object string) ([]byte, error) {
	r, err := deps.GCSClient.Bucket(deps.SessionsBucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, maxTranscriptObjectBytes))
}

// eventsFrom renders one transcript object into display events.
func eventsFrom(name transcriptName, body []byte) []TranscriptEvent {
	when := name.Time
	if name.Proposal {
		var s schema.StrategyOneOf
		pretty := string(body)
		if err := json.Unmarshal(body, &s); err == nil {
			if b, err := json.MarshalIndent(s, "", "  "); err == nil {
				pretty = string(b)
			}
		}
		return []TranscriptEvent{{Kind: "proposal", Title: "inference proposal", Body: pretty, Note: "show strategy"}}
	}
	var content genai.Content
	if err := json.Unmarshal(body, &content); err != nil {
		return []TranscriptEvent{{Kind: name.Role, Title: name.Role + " (undecodable)", At: when, Body: string(body), Note: "show raw"}}
	}
	var out []TranscriptEvent
	for _, p := range content.Parts {
		switch {
		case p == nil:
		case p.FunctionCall != nil:
			out = append(out, callEvent(p.FunctionCall, when))
		case p.FunctionResponse != nil:
			out = append(out, responseEvent(p.FunctionResponse, when))
		case p.Text != "":
			switch {
			case p.Thought:
				out = append(out, TranscriptEvent{Kind: "thought", Title: "thought", At: when, Body: p.Text, Note: "show thinking"})
			case content.Role == genai.RoleModel:
				out = append(out, TranscriptEvent{Kind: "model", Title: "model", At: when, Body: p.Text, Open: true})
			default:
				out = append(out, TranscriptEvent{Kind: "prompt", Title: "prompt", At: when, Body: p.Text, Note: "show prompt"})
			}
		}
	}
	return out
}

func callEvent(c *genai.FunctionCall, when time.Time) TranscriptEvent {
	e := TranscriptEvent{Kind: "call", Title: c.Name, At: when}
	if cmd, ok := c.Args["command"].(string); ok {
		e.Command = cmd
		if timeout, ok := c.Args["timeout_seconds"].(float64); ok {
			e.Title = fmt.Sprintf("%s (timeout %ds)", c.Name, int(timeout))
		}
	} else if len(c.Args) > 0 {
		b, _ := json.MarshalIndent(c.Args, "", "  ")
		e.Body = string(b)
		e.Note = "show args"
	}
	return e
}

func responseEvent(r *genai.FunctionResponse, when time.Time) TranscriptEvent {
	e := TranscriptEvent{Kind: "response", Title: r.Name + " result", At: when, Note: "show output"}
	if code, ok := r.Response["exit_code"].(float64); ok {
		e.Note = fmt.Sprintf("show output (exit %d)", int(code))
	}
	if msg, ok := r.Response["error"].(string); ok && msg != "" {
		e.Note = "show error"
	}
	for _, key := range []string{"output", "logs", "error"} {
		if v, ok := r.Response[key].(string); ok && v != "" {
			e.Body = v
			return e
		}
	}
	b, _ := json.MarshalIndent(r.Response, "", "  ")
	e.Body = string(b)
	return e
}
