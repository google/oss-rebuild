// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
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
// iteration's proposal, a build attempt, or a scratch exec.
type TranscriptEvent struct {
	Kind     string // prompt | model | thought | call | response | proposal | exec | build
	Title    string
	At       time.Time // zero for the proposal, which anchors its iteration
	Command  string    // call only: the command argument, shown verbatim
	Body     string
	Note     string // summary line when the body renders collapsed
	Open     bool   // render the body expanded rather than behind a toggle
	Href     string // optional link rendered after the title
	HrefText string
	Build    *BuildAttemptView // build only
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
	Iter       int // iteration by build label or time window, or 0 when unattributed
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
	composeTrajectory(data, groups, groupFor)
	for _, g := range groups {
		sortEvents(g.Events)
		data.Trajectory = append(data.Trajectory, *g)
	}
	sort.Slice(data.Trajectory, func(i, j int) bool { return data.Trajectory[i].Number < data.Trajectory[j].Number })
	return data, nil
}

// execClass buckets execs by the label their dispatcher gave them.
type execClass int

const (
	execOther          execClass = iota // a dispatch outside any build, shown as a plain exec
	execKeepAlive                       // the idle-reaper heartbeat
	execToolContainer                   // run_in_container
	execToolCheck                       // check_artifact
	execToolHost                        // run_on_host
	execBuildMachinery                  // a build's executor or comparison dispatch
)

type classifiedExec struct {
	class   execClass
	buildID string // machinery only: the rb-<id> / builds/<id> attempt
	label   string // the dispatcher's label, phase/<name> for a build phase
	script  string // the tool's command, a sh -c or docker exec script, or the argv
}

// toolClasses are the tools that dispatch execs.
var toolClasses = map[string]execClass{"run_on_host": execToolHost, "run_in_container": execToolContainer, "check_artifact": execToolCheck}

// buildSteps are the labels of a build attempt's dispatches, which group by
// build id with its phases.
var buildSteps = map[schema.ScratchLabel]bool{
	schema.ScratchLabelPrepare: true, schema.ScratchLabelStart: true, schema.ScratchLabelStop: true,
	schema.ScratchLabelImage: true, schema.ScratchLabelRun: true, schema.ScratchLabelCleanup: true, schema.ScratchLabelTiming: true,
	schema.ScratchLabelArtifact: true, schema.ScratchLabelCompare: true,
}

// sessionExecs are the labels of the dispatches outside any build.
var sessionExecs = map[schema.ScratchLabel]bool{schema.ScratchLabelPrepareCompare: true, schema.ScratchLabelRestore: true, schema.ScratchLabelReadback: true, schema.ScratchLabelCtl: true}

// buildsDirPattern finds a script's build directory. The last match wins
// because the comparison script's fetch URLs come first and may hold builds/.
var buildsDirPattern = regexp.MustCompile(`builds/([^/"]+)/`)

// classifyExec classifies an exec by the label its dispatcher gave it. The
// argv still supplies the build id and the script, which the label does not
// carry. The second result is false for a label the page does not know, an
// empty one included.
func classifyExec(e schema.ScratchExec) (classifiedExec, bool) {
	c := classifiedExec{class: execOther, label: string(e.Label), script: scriptOf(e.Cmd)}
	if _, ok := e.Label.Phase(); ok || buildSteps[e.Label] {
		c.class, c.buildID = execBuildMachinery, buildIDOf(e.Cmd)
		return c, true
	}
	if tool, ok := e.Label.Tool(); ok {
		class, known := toolClasses[tool]
		if !known {
			return c, false
		}
		c.class = class
		if class == execToolHost {
			c.script = hostToolCommand(c.script)
		}
		return c, true
	}
	switch {
	case e.Label == schema.ScratchLabelKeepAlive:
		c.class = execKeepAlive
	case !sessionExecs[e.Label]:
		return c, false
	}
	return c, true
}

// buildIDOf finds the build an exec belongs to: the rb-<id> container of a
// docker argv, else the last builds/<id>/ path in its script.
func buildIDOf(cmd []string) string {
	if len(cmd) > 0 && cmd[0] == "docker" {
		for _, a := range cmd {
			if id, ok := strings.CutPrefix(a, "rb-"); ok && id != "" {
				return id
			}
		}
	}
	if m := buildsDirPattern.FindAllStringSubmatch(strings.Join(cmd, " "), -1); m != nil {
		return m[len(m)-1][1]
	}
	return ""
}

// scriptOf is what an exec ran, for display: a docker exec's or sh -c's
// script, else the argv.
func scriptOf(cmd []string) string {
	switch {
	case len(cmd) > 2 && cmd[0] == "docker" && cmd[1] == "exec":
		return cmd[len(cmd)-1]
	case len(cmd) == 3 && (cmd[0] == "/bin/sh" || cmd[0] == "sh") && cmd[1] == "-c":
		return cmd[2]
	}
	return strings.Join(cmd, " ")
}

// hostToolCommand unwraps the model's command from run_on_host's output
// envelope (internal/agent detachOutput). Other scripts return as is.
func hostToolCommand(script string) string {
	if body, ok := strings.CutPrefix(script, "( "); ok {
		if i := strings.LastIndex(body, "\n) >/tmp/.rb-tool-"); i >= 0 {
			return body[:i]
		}
	}
	return script
}

// BuildStep is one machinery dispatch of a build attempt.
type BuildStep struct {
	Label    string
	Script   string
	State    schema.ScratchExecState
	ExitCode int
	Duration string
	OutPath  string
}

// BuildAttemptView is one build's dispatches in order.
type BuildAttemptView struct {
	ID      string
	Iter    int       // from the build label, else the window covering At, else 0
	At      time.Time // creation of the first dispatch
	Outcome string
	Steps   []BuildStep
}

// iterOfBuildID maps build labels iter-<n> and iter-<n>-canon to iteration n.
var iterOfBuildID = regexp.MustCompile(`^iter-(\d+)(?:-canon)?$`)

// composeTrajectory adds the exec ledger to the iteration groups. Machinery
// collapses into one attempt per build. Keepalives, tool execs the transcript
// shows as a call, and execs outside every window stay in the ledger only.
func composeTrajectory(data *SessionData, groups map[int]*IterationGroup, groupFor func(int) *IterationGroup) {
	hasTranscript := false
	for _, g := range groups {
		for _, e := range g.Events {
			hasTranscript = hasTranscript || !e.At.IsZero() || e.Kind == "proposal"
		}
	}
	windows := newIterationWindows(groups)
	var attempts []*BuildAttemptView
	byID := make(map[string]*BuildAttemptView)
	unknown := map[string]int{}
	for i := range data.Execs {
		v := &data.Execs[i]
		v.Iter = windows.covering(v.CreatedAt)
		cls, ok := classifyExec(v.ScratchExec)
		if !ok {
			unknown[string(v.Label)]++
		}
		switch cls.class {
		case execKeepAlive:
			continue
		case execBuildMachinery:
			id := cmp.Or(cls.buildID, "build")
			a := byID[id]
			if a == nil {
				a = &BuildAttemptView{ID: id, Iter: v.Iter, At: v.CreatedAt}
				if m := iterOfBuildID.FindStringSubmatch(id); m != nil {
					a.Iter, _ = strconv.Atoi(m[1])
				}
				byID[id] = a
				attempts = append(attempts, a)
			}
			if a.Iter > 0 {
				v.Iter = a.Iter
			}
			a.Steps = append(a.Steps, BuildStep{Label: cls.label, Script: cls.script, State: v.State, ExitCode: v.ExitCode, Duration: v.Duration, OutPath: v.OutPath})
			continue
		case execToolContainer, execToolCheck, execToolHost:
			// The transcript shows these as calls.
			if hasTranscript {
				continue
			}
		}
		if v.Iter > 0 {
			g := groupFor(v.Iter)
			g.Events = append(g.Events, execEvent(*v, cls.script))
		}
	}
	for _, a := range attempts {
		if a.Iter == 0 {
			continue
		}
		a.Outcome = buildOutcome(a.Steps)
		g := groupFor(a.Iter)
		g.Events = append(g.Events, TranscriptEvent{
			Kind:  "build",
			Title: fmt.Sprintf("build %s · %s", a.ID, a.Outcome),
			At:    a.At,
			Build: a,
		})
	}
	for _, l := range slices.Sorted(maps.Keys(unknown)) {
		switch n := unknown[l]; {
		case l != "":
			data.Warnings = append(data.Warnings, fmt.Sprintf("unknown exec label %q", l))
		case n == 1:
			data.Warnings = append(data.Warnings, "1 exec without a label")
		default:
			data.Warnings = append(data.Warnings, fmt.Sprintf("%d execs without a label", n))
		}
	}
}

// window starts an iteration's slice of session time at its group's earliest
// event or record creation. The slice ends where the next window starts.
type window struct {
	num   int
	start time.Time
}

type iterationWindows []window

func newIterationWindows(groups map[int]*IterationGroup) iterationWindows {
	var ws iterationWindows
	for n, g := range groups {
		var start time.Time
		if g.Record != nil {
			start = g.Record.Created
		}
		for _, e := range g.Events {
			if !e.At.IsZero() && (start.IsZero() || e.At.Before(start)) {
				start = e.At
			}
		}
		if !start.IsZero() {
			ws = append(ws, window{n, start})
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].start.Before(ws[j].start) })
	return ws
}

// covering returns the iteration whose window covers t, or 0 when none does.
func (ws iterationWindows) covering(t time.Time) int {
	iter := 0
	for _, w := range ws {
		if t.Before(w.start) {
			break
		}
		iter = w.num
	}
	return iter
}

// buildOutcome summarizes an attempt's steps. The agent runs the comparison
// only on a mismatch, and its exits above 1 are setup failures.
func buildOutcome(steps []BuildStep) string {
	for _, s := range steps {
		switch {
		case s.State == schema.ScratchExecPending:
			return "running"
		case s.State != schema.ScratchExecCompleted:
			return fmt.Sprintf("%s %s", s.Label, s.State)
		case s.Label == string(schema.ScratchLabelCompare) && s.ExitCode <= 1:
			return "mismatch"
		case s.ExitCode != 0:
			return fmt.Sprintf("%s exit %d", s.Label, s.ExitCode)
		}
	}
	for _, s := range steps {
		if s.Label == string(schema.ScratchLabelArtifact) {
			return "succeeded"
		}
	}
	return "completed"
}

// execEvent renders an exec's ledger row as a trajectory event: its label,
// state, exit code, and duration on the worker's clock, the script it ran,
// and a link to its raw output.
func execEvent(v ExecView, script string) TranscriptEvent {
	parts := []string{"exec"}
	if v.Label != "" {
		parts = append(parts, string(v.Label))
	}
	parts = append(parts, string(v.State))
	switch v.State {
	case schema.ScratchExecCompleted, schema.ScratchExecTimedOut:
		parts = append(parts, fmt.Sprintf("exit %d", v.ExitCode), v.Duration)
	}
	note := script
	if len(note) > 100 {
		note = note[:100] + "..."
	}
	body := script
	if v.ErrorMsg != "" {
		body += "\n\n# error: " + v.ErrorMsg
	}
	return TranscriptEvent{
		Kind:     "exec",
		Title:    strings.Join(parts, " · "),
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
// 0-proposal.json or 0-inference-proposal.json.
type transcriptName struct {
	Iter      int
	Time      time.Time
	Part      int
	Role      string
	Proposal  bool
	Inference bool // the proposal came from heuristic inference, not the LLM
}

// transcriptNameRE matches a transcript object name relative to "messages/".
var transcriptNameRE = regexp.MustCompile(`^(\d+)/(?:0-(inference-)?proposal|(.+)-(\d+)-([a-z]+))\.json$`)

// parseTranscriptName parses a transcript object name, reporting ok=false
// for objects under the prefix that are not transcript messages.
func parseTranscriptName(name string) (transcriptName, bool) {
	m := transcriptNameRE.FindStringSubmatch(name)
	if m == nil {
		return transcriptName{}, false
	}
	iter, _ := strconv.Atoi(m[1])
	if m[3] == "" {
		return transcriptName{Iter: iter, Proposal: true, Inference: m[2] != ""}, true
	}
	// RFC3339Nano strips trailing zeros, so lexicographic order of the
	// prefix is not chronological. Callers must sort on the parsed time.
	when, err := time.Parse(time.RFC3339Nano, m[3])
	if err != nil {
		return transcriptName{}, false
	}
	part, _ := strconv.Atoi(m[4])
	return transcriptName{Iter: iter, Time: when, Part: part, Role: m[5]}, true
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
		title := "proposed build"
		if name.Inference {
			title = "inference proposal"
		}
		return []TranscriptEvent{{Kind: "proposal", Title: title, Body: pretty, Note: "show strategy"}}
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
