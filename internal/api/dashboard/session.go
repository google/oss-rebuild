// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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
// prompt, model prose or thought, a tool call, a tool response, the
// first-iteration inference proposal, or a scratch exec woven in by time.
type TranscriptEvent struct {
	Kind     string // prompt | model | thought | call | response | proposal | exec
	Title    string
	At       time.Time // zero for the proposal, which anchors its iteration
	Command  string    // call only: the command argument, shown verbatim
	Body     string
	Note     string // summary line when the body renders collapsed
	Open     bool   // render the body expanded rather than behind a toggle
	Href     string // optional link rendered after the title
	HrefText string
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

// ExecView pairs a scratch exec with its pre-formatted display fields.
type ExecView struct {
	schema.ScratchExec
	Iter       int // iteration attributed by time window, or 0 when unattributed
	CmdLine    string
	Queued     string
	Duration   string
	ErrorMsg   string
	StateClass string // status-success | status-fail | status-running
	OutPath    string // dashboard route streaming the raw output object
}

type SessionData struct {
	Session    SessionView
	Duration   string
	Usage      string
	Trajectory []IterationGroup
	Execs      []ExecView
	Warnings   []string
}

// Session renders one agent session's trajectory: the session record joined
// with its iteration records, the chat transcript dump, and the scratch exec
// ledger.
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
	// The transcript and the exec ledger live in different stores and
	// neither load informs the other, so they run together.
	var (
		wg            sync.WaitGroup
		transcriptErr error
		execs         []schema.ScratchExec
		execErr       error
	)
	if deps.GCSClient != nil && deps.SessionsBucket != "" {
		wg.Go(func() { transcriptErr = loadTranscript(ctx, deps, s.ID, groupFor) })
	} else {
		data.Warnings = append(data.Warnings, "transcripts not configured: pass -sessions-bucket")
	}
	if s.ScratchID != "" && deps.Execs != nil {
		wg.Go(func() { execs, execErr = deps.Execs.ListByScratch(ctx, s.ScratchID) })
	}
	wg.Wait()
	if transcriptErr != nil {
		data.Warnings = append(data.Warnings, fmt.Sprintf("loading transcript: %v", transcriptErr))
	}
	if execErr != nil {
		data.Warnings = append(data.Warnings, fmt.Sprintf("loading scratch execs: %v", execErr))
	}
	for _, e := range execs {
		data.Execs = append(data.Execs, newExecView(s.ID, e))
	}
	attributeExecs(data.Execs, groups)
	for _, g := range groups {
		sortEvents(g.Events)
		data.Trajectory = append(data.Trajectory, *g)
	}
	sort.Slice(data.Trajectory, func(i, j int) bool { return data.Trajectory[i].Number < data.Trajectory[j].Number })
	return data, nil
}

// attributeExecs assigns each exec to the iteration whose time window covers
// its creation and places them into that group's events. A window opens at the
// group's earliest timestamped moment (transcript event, or the iteration
// record's creation as fallback) and closes when the next one opens. Execs
// predating every window fall to the initial group, run before any chat,
// anchored only by its untimestamped proposal.
func attributeExecs(execs []ExecView, groups map[int]*IterationGroup) {
	type window struct {
		num   int
		start time.Time
	}
	var windows []window
	lowest := 0
	for n, g := range groups {
		if lowest == 0 || n < lowest {
			lowest = n
		}
		var start time.Time
		if g.Record != nil && !g.Record.Created.IsZero() {
			start = g.Record.Created
		}
		for _, e := range g.Events {
			if !e.At.IsZero() && (start.IsZero() || e.At.Before(start)) {
				start = e.At
			}
		}
		if !start.IsZero() {
			windows = append(windows, window{num: n, start: start})
		}
	}
	if len(windows) == 0 {
		return
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].start.Before(windows[j].start) })
	for i := range execs {
		e := &execs[i]
		iter := lowest
		for _, w := range windows {
			if e.CreatedAt.Before(w.start) {
				break
			}
			iter = w.num
		}
		e.Iter = iter
		groups[iter].Events = append(groups[iter].Events, execEvent(*e))
	}
}

// execEvent renders one ledger row: the exec's state, exit code, duration
// on the worker's clock, full argv, and a link to its raw output.
func execEvent(v ExecView) TranscriptEvent {
	title := fmt.Sprintf("exec · %s", v.State)
	switch v.State {
	case schema.ScratchExecCompleted, schema.ScratchExecTimedOut:
		title = fmt.Sprintf("exec · %s · exit %d · %s", v.State, v.ExitCode, v.Duration)
	}
	note := v.CmdLine
	if len(note) > 100 {
		note = note[:100] + "..."
	}
	body := v.CmdLine
	if v.ErrorMsg != "" {
		body += "\n\n# error: " + v.ErrorMsg
	}
	return TranscriptEvent{
		Kind:     "exec",
		Title:    title,
		At:       v.CreatedAt,
		Note:     note,
		Body:     body,
		Href:     v.OutPath,
		HrefText: "raw output",
	}
}

// sortEvents orders a group's events chronologically, keeping untimestamped
// entries (e.g. the inference proposal) first.
func sortEvents(events []TranscriptEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i].At, events[j].At
		if a.IsZero() != b.IsZero() {
			return a.IsZero()
		}
		return a.Before(b)
	})
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

func newExecView(sessionID string, e schema.ScratchExec) ExecView {
	v := ExecView{
		ScratchExec: e,
		CmdLine:     strings.Join(e.Cmd, " "),
		Duration:    "N/A",
	}
	if !e.CreatedAt.IsZero() {
		v.Queued = e.CreatedAt.Format("15:04:05")
	}
	if !e.StartedAt.IsZero() && !e.FinishedAt.IsZero() {
		v.Duration = e.FinishedAt.Sub(e.StartedAt).Round(time.Millisecond).String()
	}
	if e.Error != nil {
		v.ErrorMsg = e.Error.Message
	}
	switch {
	case e.State == schema.ScratchExecPending:
		v.StateClass = "status-running"
	case e.State == schema.ScratchExecCompleted && e.ExitCode == 0:
		v.StateClass = "status-success"
	default:
		v.StateClass = "status-fail"
	}
	if e.OutURI != "" {
		v.OutPath = fmt.Sprintf("/session/%s/exec/%s/out", sessionID, e.ID)
	}
	return v
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

// HandleRawExecOutput streams one exec's merged output object. Outside the
// act/api framework (like HandleRawLogs) since it writes a raw body.
func HandleRawExecOutput(w http.ResponseWriter, r *http.Request, sessionID, execID string, deps *Deps) {
	if deps.GCSClient == nil || deps.Execs == nil {
		http.Error(w, "exec output viewing not configured", http.StatusServiceUnavailable)
		return
	}
	exec, err := deps.Execs.Get(r.Context(), execID)
	if err != nil {
		http.Error(w, "exec not found", http.StatusNotFound)
		return
	}
	// The exec is addressed under a session URL, so refuse IDs that
	// belong to a different session's scratch.
	sessions, err := deps.Sessions.FetchSessions(r.Context(), &rundex.FetchSessionsReq{IDs: []string{sessionID}})
	if err != nil || len(sessions) == 0 || sessions[0].ScratchID != exec.ScratchID {
		http.Error(w, "exec not found for session", http.StatusNotFound)
		return
	}
	rest, ok := strings.CutPrefix(exec.OutURI, "gs://")
	if !ok {
		http.Error(w, "no output recorded for exec", http.StatusNotFound)
		return
	}
	bucket, object, ok := strings.Cut(rest, "/")
	if !ok {
		http.Error(w, "malformed output URI", http.StatusInternalServerError)
		return
	}
	reader, err := deps.GCSClient.Bucket(bucket).Object(object).NewReader(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read output: %v", err), http.StatusNotFound)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("streaming exec output %s: %v", exec.ID, err)
	}
}
