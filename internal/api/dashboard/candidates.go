// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	bash "github.com/msuozzo/bonsai/bonsai-bash"
)

// Candidate is one attempt at the artifact: a build the job ran or a build
// the agent ran, and how far it got.
type Candidate struct {
	N        int
	At       time.Time
	Change   string // the build commands the agent ran, or what the job built
	Command  string // the agent's full command, or the job build's id
	Took     string // empty when unmeasured
	Stages   []Stage
	Compare  *Comparison
	Review   *ReviewView
	ReviewAt time.Time
	After    []Call // the tool calls and records up to the next candidate
	Job      bool   // the job built it rather than the agent
	took     time.Duration
	exit     string // the agent build's result, as toolResult renders it
}

// Outcome says in a few words how far the candidate got.
func (c *Candidate) Outcome() string {
	st := c.Stages
	switch {
	case st[stageVerify].State == "ok":
		return "Matched, the review accepted it, and the job's own build reproduced it"
	case st[stageVerify].State != "none":
		return "Matched and accepted, but the job's own build did not reproduce it"
	case c.Review != nil && c.Review.Refused():
		return "Matched, but the review refused it: " + strings.Join(c.Review.Rules(), ", ")
	case c.Review != nil:
		return "Matched and accepted"
	case c.Compare != nil && c.Compare.Err != "":
		return "Could not be compared"
	case c.Compare != nil && c.Compare.Match:
		return "Matched, not handed off"
	case c.Compare != nil:
		return "Compared: " + strings.ToLower(c.Compare.Chip())
	case st[stageBuild].State == "timeout":
		return "Timed out building after " + st[stageBuild].Label
	case st[stageBuild].State == "failed", st[stageSource].State == "failed", st[stageDeps].State == "failed":
		return strings.TrimSpace("The build failed " + c.exit)
	case st[stageArtifact].State == "ok":
		return "Built an artifact, never compared"
	case st[stageBuild].State == "ok":
		return "Built, never packaged or compared"
	}
	return "Still running"
}

// Verdict is how the session ended, shown as the candidate table's last row.
type Verdict struct {
	Kind      string // as SessionView.Verdict sorts it
	At        time.Time
	Line      string     // the ending in a line
	Summary   string     // the session's completion summary
	Closest   *Candidate // the compared mismatch with the fewest entries
	Matched   *Candidate // the last match, when the review accepted none
	Accepted  *Candidate
	Remaining string // give_up's account of what still differs
	Commands  string // give_up's commands for the closest candidate
}

// Label is Kind as a row title.
func (v Verdict) Label() string { return strings.ToUpper(v.Kind[:1]) + v.Kind[1:] }

// Tone is Kind as the status color classes name it.
func (v Verdict) Tone() string {
	switch v.Kind {
	case "reproduced":
		return "ok"
	case "gave up", "timed out":
		return "warn"
	case "running":
		return "live"
	}
	return "bad"
}

// StageNames heads the stage columns, in Candidate.Stages order.
var StageNames = []string{"Source", "Deps", "Build", "Artifact", "Compare", "Review", "Verify"}

const (
	stageSource = iota
	stageDeps
	stageBuild
	stageArtifact
	stageCompare
	stageReview
	stageVerify
)

// Stage is one cell of a candidate's row.
type Stage struct {
	State string // ok | reused | timeout | failed | off | match | none
	Label string
}

var (
	stageNone   = Stage{"none", "–"}
	stageOK     = Stage{"ok", "✓"}
	stageReused = Stage{"reused", "✓"}
	stageFailed = Stage{"failed", "✕"}
	stageMatch  = Stage{"match", "="}
)

// Comparison is a candidate's comparison with upstream: the job's diff
// summary or check_artifact's result.
type Comparison struct {
	By        string // job compare | check_artifact
	At        time.Time
	Match     bool
	Err       string // why the comparison could not run
	Entries   int    // differing entries, 0 on a match or when only bytes differ
	Candidate string // stabilized sha256, abbreviated
	Upstream  string
	Groups    []DiffGroup
	Note      string
	Path      string // the candidate artifact check_artifact was given
	Diffr     *Call  // the agent's own diffr run on these artifacts
	forms     string // a path naming the candidate's stabilized form
}

// DiffGroup is one status group of a diff summary.
type DiffGroup struct {
	Label   string
	Kind    string // differ | upstream | candidate: which side has the entries
	Mark    string // ≠ | + | −, as diffr marks the sides
	Entries []DiffEntry
}

// DiffEntry is one entry of a diff summary, with the comments diffr made.
type DiffEntry struct{ Path, Note string }

// Chip sums up the comparison in a few words.
func (c *Comparison) Chip() string {
	switch {
	case c.Err != "":
		return "Not compared"
	case c.Match:
		return "Match"
	case c.Entries == 1:
		return "1 entry off"
	case c.Entries > 0:
		return fmt.Sprintf("%d entries off", c.Entries)
	}
	return "Bytes differ"
}

// Call is one tool call or record between candidates.
type Call struct {
	ID     string // the row's anchor
	Drawer int    // the candidate whose drawer lists it, 0 before the first
	At     time.Time
	Tool   string
	Cmd    string
	Result string
	Detail string // the error behind an error Result
	Bad    bool   // nonzero, timed out, or refused
	Review bool   // the row opens its candidate's review
}

// stretch is one span of session time before layoutStrip places it.
type stretch struct {
	kind, title string
	from, to    time.Time
}

// Strip is the session's time drawn as one bar, a span per activity.
type Strip struct {
	Start, End time.Time
	Spans      []Span
	Ticks      []Tick
	Totals     []Total
}

// Span is one stretch of the strip. Left and Width are percentages.
type Span struct {
	Kind, Title, Left, Width string
}

// Tick is one time label under the strip.
type Tick struct{ Label, Left string }

// Total is the time one kind of span adds up to.
type Total struct{ Kind, Label, Took string }

var (
	timedOutText = regexp.MustCompile(`timed out after (\d+)s`)
	shaInResult  = regexp.MustCompile(`\(candidate ([0-9a-f]{8})[0-9a-f]*, upstream ([0-9a-f]{8})[0-9a-f]*\)`)
	shaOnMatch   = regexp.MustCompile(`\(sha256 ([0-9a-f]{8})[0-9a-f]*\)`)
	formsInText  = regexp.MustCompile(`Stabilized forms: (\S+)`)
	closestText  = regexp.MustCompile(`Closest so far in this session: (\d+) differing entr`)
	groupHead    = regexp.MustCompile(`^(differ|only in (\S+)) \((\d+)\):$`)
)

// lastCommand is the last simple command of cmd, the one a build or package
// call is named after.
func lastCommand(cmd string) string {
	src := []byte(cmd)
	root, err := bash.NewParser().Parse(src)
	if err != nil {
		return strings.TrimSpace(cmd)
	}
	var last *bash.Node
	for c := range root.Find(bash.KindCommand) {
		if last == nil || c.StartByte >= last.EndByte { // not a substitution inside last
			last = c
		}
	}
	if last == nil {
		return strings.TrimSpace(cmd)
	}
	return string(last.Text(src))
}

// toolNames shortens the agent's tool names for the call rows.
var toolNames = map[string]string{"run_in_container": "container", "run_on_host": "host", "read_logs_end": "logs"}

// argsLine renders a call's arguments on one line: what it names first, as
// set_source_ref's ref and read_repo_file's path, then the rest.
func argsLine(args map[string]any) string {
	var parts []string
	for _, k := range []string{"ref", "path"} {
		if v, ok := args[k].(string); ok {
			parts = append(parts, v)
		}
	}
	var rest []string
	for k, v := range args {
		if k != "ref" && k != "path" {
			rest = append(rest, fmt.Sprintf("%s: %v", k, v))
		}
	}
	slices.Sort(rest)
	return strings.Join(append(parts, rest...), " · ")
}

// toolResult renders a tool response's outcome and whether it went wrong.
func toolResult(r map[string]any) (string, bool, int) {
	if r == nil {
		return "", false, 0
	}
	if msg, _ := r["error"].(string); msg != "" {
		if m := timedOutText.FindStringSubmatch(msg); m != nil {
			n, _ := strconv.Atoi(m[1])
			return "timed out " + m[1] + "s", true, n
		}
		return "error", true, 0
	}
	if code, ok := r["exit_code"].(float64); ok {
		return fmt.Sprintf("exit %d", int(code)), code != 0, 0
	}
	return "", false, 0
}

// parseDiffSummary reads diffr's file-level summary out of text: each
// "within" block's status groups and their entries. Nested archives name
// their archive in the group label.
func parseDiffSummary(text string) []DiffGroup {
	var groups []DiffGroup
	var archive string
	depth := -1
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case strings.HasPrefix(t, "within ") && strings.HasSuffix(t, ":"):
			name := strings.TrimSuffix(strings.TrimPrefix(t, "within "), ":")
			name, _, _ = strings.Cut(name, " (vs ")
			if depth < 0 {
				depth, archive = indent, ""
			} else {
				archive = name
			}
		case depth < 0:
		case groupHead.MatchString(t):
			m := groupHead.FindStringSubmatch(t)
			g := DiffGroup{Label: t[:len(t)-1], Kind: "differ", Mark: "≠"}
			if m[2] == "upstream" {
				g.Kind, g.Mark = "upstream", "+"
			} else if m[2] != "" {
				g.Kind, g.Mark = "candidate", "−"
			}
			if archive != "" {
				g.Label = archive + ": " + g.Label
			}
			groups = append(groups, g)
		case t == "" || indent <= depth:
			if len(groups) > 0 {
				return groups
			}
		case len(groups) > 0:
			path, note, _ := strings.Cut(t, " (")
			groups[len(groups)-1].Entries = append(groups[len(groups)-1].Entries, DiffEntry{path, strings.TrimSuffix(note, ")")})
		}
	}
	return groups
}

func entryCount(groups []DiffGroup) int {
	var n int
	for _, g := range groups {
		n += len(g.Entries)
	}
	return n
}

func checkComparison(at time.Time, r map[string]any) *Comparison {
	c := &Comparison{By: "check_artifact", At: at}
	if msg, _ := r["error"].(string); msg != "" {
		c.Err = msg
		return c
	}
	result, _ := r["result"].(string)
	c.Match, _ = r["match"].(bool)
	if m := shaOnMatch.FindStringSubmatch(result); c.Match && m != nil {
		c.Candidate, c.Upstream = m[1], m[1]
	}
	if m := shaInResult.FindStringSubmatch(result); m != nil {
		c.Candidate, c.Upstream = m[1], m[2]
	}
	if m := formsInText.FindStringSubmatch(result); m != nil {
		c.forms = m[1]
	}
	if m := closestText.FindStringSubmatch(result); m != nil {
		c.Note = "Closest so far: " + m[1] + " entries."
	}
	c.Groups = parseDiffSummary(result)
	c.Entries = entryCount(c.Groups)
	return c
}

// jobStages reads a job build's stages from its dispatches, each phase by
// its label. A plan without a deps phase installs them in its build phase.
func jobStages(b *BuildAttemptView) []Stage {
	st := slices.Repeat([]Stage{stageNone}, len(StageNames))
	of := func(s BuildStep) Stage {
		switch {
		case s.State == schema.ScratchExecTimedOut:
			return Stage{"timeout", s.Duration}
		case s.State != schema.ScratchExecCompleted:
			return Stage{"none", "…"}
		case s.Label == string(schema.ScratchLabelCompare) && s.ExitCode == 1:
			return Stage{"off", "≠"}
		case s.ExitCode != 0:
			return stageFailed
		}
		return stageOK
	}
	slot := map[rebuild.BuildPhase]int{rebuild.PhaseSetup: stageSource, rebuild.PhaseSource: stageSource, rebuild.PhaseDeps: stageDeps, rebuild.PhaseBuild: stageBuild}
	hasDeps := false
	for _, s := range b.Steps {
		switch l := schema.ScratchLabel(s.Label); l {
		case schema.ScratchLabelPrepare, schema.ScratchLabelStart:
			if of(s) != stageOK {
				st[stageSource] = of(s)
			}
		case schema.ScratchLabelArtifact:
			st[stageArtifact] = of(s)
		case schema.ScratchLabelCompare:
			if d := of(s); d == stageOK {
				st[stageCompare] = stageMatch
			} else {
				st[stageCompare] = d
			}
		default:
			phase, ok := l.Phase()
			i, known := slot[phase]
			if !ok || !known {
				continue
			}
			hasDeps = hasDeps || phase == rebuild.PhaseDeps
			if st[i] == stageNone || st[i] == stageOK {
				st[i] = of(s)
			}
		}
	}
	if !hasDeps {
		st[stageDeps] = st[stageBuild]
	}
	if st[stageCompare] == stageNone && st[stageArtifact] == stageOK && buildOutcome(b.Steps) == "succeeded" {
		st[stageCompare] = stageMatch
	}
	return st
}

// buildSpan bounds a job build on the worker's clock, falling back to its
// dispatch time.
func buildSpan(b *BuildAttemptView) (time.Time, time.Time) {
	start, end := b.At, b.At
	for _, s := range b.Steps {
		if !s.Started.IsZero() && (start.IsZero() || s.Started.Before(start)) {
			start = s.Started
		}
		if s.Finished.After(end) {
			end = s.Finished
		}
	}
	return start, end
}

// deriveCandidates fills in the session's candidates from its trajectory,
// with the calls before the first one, the time strip, and the verdict.
func deriveCandidates(data *SessionData, start, end time.Time) {
	d := &deriver{
		reply:     map[*TranscriptEvent]*TranscriptEvent{},
		proposals: map[int]*TranscriptEvent{},
		records:   map[int]*schema.AgentIteration{},
	}
	for gi := range data.Trajectory {
		g := &data.Trajectory[gi]
		d.records[g.Number] = g.Record
		for i := range g.Events {
			if e := &g.Events[i]; e.Kind == "proposal" {
				d.proposals[g.Number] = e
			} else if !e.At.IsZero() {
				d.events = append(d.events, e)
			}
		}
	}
	slices.SortStableFunc(d.events, func(a, b *TranscriptEvent) int { return a.At.Compare(b.At) })
	// Responses follow their calls in order, so each pairs with the earliest
	// unanswered call of its tool.
	waiting := map[string][]*TranscriptEvent{}
	for _, e := range d.events {
		switch e.Kind {
		case "call":
			waiting[e.Tool] = append(waiting[e.Tool], e)
		case "response":
			if q := waiting[e.Tool]; len(q) > 0 {
				d.reply[q[0]], waiting[e.Tool] = e, q[1:]
			}
		}
	}
	for i, e := range d.events {
		switch e.Kind {
		case "build":
			if e.Build != nil {
				d.jobBuild(e, d.events[i+1:], end)
			}
		case "call":
			d.call(e)
		case "review":
			if e.Review != nil && !e.Review.Carried {
				d.review(e)
			}
		case "formalize":
			d.add(e, Call{Tool: "formalize", Cmd: strings.TrimPrefix(e.Title, "formalization ")})
		case "giveup":
			reason, _, _ := strings.Cut(strings.TrimSpace(e.Body), "\n")
			d.add(e, Call{Tool: "give up", Cmd: reason, Bad: true})
		}
	}
	d.flush()
	for _, c := range d.cands {
		if c.took > 0 {
			c.Took = humanDuration(c.took)
		}
		if c.Compare == nil || c.Compare.forms == "" {
			continue
		}
		// The agent's own diffr run on this candidate is the first host call
		// after it that names its stabilized form.
		for _, later := range d.cands[c.N-1:] {
			if i := slices.IndexFunc(later.After, func(call Call) bool {
				return call.Tool == "host" && strings.Contains(call.Cmd, "diffr") && strings.Contains(call.Cmd, c.Compare.forms)
			}); i >= 0 {
				c.Compare.Diffr = &later.After[i]
				break
			}
		}
	}
	data.Candidates, data.Lead, data.Strip = d.cands, d.lead, layoutStrip(d.spans, start, end, d.first)
	data.Verdict = sessionVerdict(data.Session, d.cands, d.giveUp, end)
}

// deriver walks a session's events in time order. Each build opens a
// candidate, and the rows after it go in its drawer.
type deriver struct {
	events    []*TranscriptEvent                    // the timed events in order
	reply     map[*TranscriptEvent]*TranscriptEvent // each answered call's response
	proposals map[int]*TranscriptEvent              // by iteration
	records   map[int]*schema.AgentIteration        // by iteration
	cands     []*Candidate
	lead      []Call
	spans     []stretch
	first     time.Time      // the agent's first call
	giveUp    map[string]any // give_up's arguments
	rows      int

	cur      *Candidate         // the latest candidate, whose drawer takes new rows
	matched  *Candidate         // the last match no review has refused
	accepted *Candidate         // accepted and awaiting the job's verification build
	work     []*TranscriptEvent // container calls since the last candidate
}

// add puts a row in the current candidate's drawer, or before the first.
func (d *deriver) add(e *TranscriptEvent, c Call) {
	c.ID, c.At = fmt.Sprintf("call-%d", d.rows), e.At
	d.rows++
	if d.cur == nil {
		d.lead = append(d.lead, c)
		return
	}
	c.Drawer = d.cur.N
	d.cur.After = append(d.cur.After, c)
}

func (d *deriver) open(at time.Time, change, command string) *Candidate {
	c := &Candidate{N: len(d.cands) + 1, At: at, Change: change, Command: command, Stages: slices.Repeat([]Stage{stageNone}, len(StageNames))}
	d.cands = append(d.cands, c)
	d.cur = c
	return c
}

// result reads a call's response: what it says, whether it went wrong, and
// the seconds it ran before timing out.
func (d *deriver) result(call *TranscriptEvent) (resp *TranscriptEvent, result string, bad bool, timeout int) {
	if resp = d.reply[call]; resp != nil {
		result, bad, timeout = toolResult(resp.Response)
	}
	return resp, result, bad, timeout
}

// purposeStages are the stage cells a call's purposes fill. A failed call
// fails only the first cell of each.
var purposeStages = map[schema.Purpose][]int{
	schema.PurposeSource:  {stageSource},
	schema.PurposeDeps:    {stageDeps},
	schema.PurposeBuild:   {stageBuild},
	schema.PurposePackage: {stageBuild, stageArtifact},
}

func (d *deriver) call(e *TranscriptEvent) {
	if d.first.IsZero() {
		d.first = e.At
	}
	switch e.Tool {
	case "check_artifact":
		d.check(e)
		return
	case "handoff":
		summary, _ := e.Args["summary"].(string)
		summary, _, _ = strings.Cut(strings.TrimSpace(summary), "\n")
		d.add(e, Call{Tool: "handoff", Cmd: summary})
		return
	case "give_up":
		d.giveUp = e.Args // its record follows
		return
	case "run_in_container":
		d.work = append(d.work, e)
	}
	resp, result, bad, timeout := d.result(e)
	var detail string
	if resp != nil {
		detail, _ = resp.Response["error"].(string)
	}
	if timeout > 0 && !isBuild(e) {
		d.spans = append(d.spans, stretch{"timeout", "a call timed out", e.At, resp.At})
	}
	d.add(e, Call{Tool: cmp.Or(toolNames[e.Tool], e.Tool), Cmd: strings.TrimSpace(cmp.Or(e.Command, argsLine(e.Args))), Result: result, Detail: detail, Bad: bad})
}

func isBuild(e *TranscriptEvent) bool {
	return e.Purpose == schema.PurposeBuild || e.Purpose == schema.PurposePackage
}

// agentCandidate opens a candidate for the agent's container work since the
// last one, named by the builds that succeeded. Each call marks the stages
// its purposes name with how it went.
func (d *deriver) agentCandidate(at time.Time) *Candidate {
	c := d.open(at, "check of an artifact built earlier", "")
	c.Stages[stageSource], c.Stages[stageDeps] = stageReused, stageReused
	var names, cmds []string
	for _, w := range d.work {
		resp, result, bad, timeout := d.result(w)
		if isBuild(w) && resp != nil {
			kind := "build"
			if timeout > 0 {
				kind = "timeout"
			}
			c.took, c.exit = c.took+resp.At.Sub(w.At), result
			d.spans = append(d.spans, stretch{kind, fmt.Sprintf("candidate %d", c.N), w.At, resp.At})
		}
		switch {
		case result == "":
		case bad:
			st := stageFailed
			if timeout > 0 {
				st = Stage{"timeout", strconv.Itoa(timeout) + "s"}
			}
			for _, p := range w.All {
				if i := purposeStages[p]; len(i) > 0 {
					c.Stages[i[0]] = st
				}
			}
		default:
			for _, p := range w.All {
				for _, i := range purposeStages[p] {
					c.Stages[i] = stageOK
				}
			}
			if isBuild(w) {
				// A build run again moves to the end, as its last run is the one that counts.
				name := lastCommand(w.Command)
				if i := slices.Index(names, name); i >= 0 {
					names, cmds = slices.Delete(names, i, i+1), slices.Delete(cmds, i, i+1)
				}
				names, cmds = append(names, name), append(cmds, strings.TrimSpace(w.Command))
			}
		}
	}
	if len(names) > 0 {
		c.Change, c.Command = strings.Join(names, " && "), strings.Join(cmds, "\n")
	}
	d.work = nil
	return c
}

// flush opens a candidate for container work that built something but was
// never checked.
func (d *deriver) flush() {
	if i := slices.IndexFunc(d.work, isBuild); i >= 0 {
		last := d.work[len(d.work)-1]
		d.agentCandidate(last.At)
	}
	d.work = nil
}

// check opens a candidate for the work since the last one, compared by
// check_artifact.
func (d *deriver) check(e *TranscriptEvent) {
	c := d.agentCandidate(e.At)
	resp := d.reply[e]
	if resp == nil {
		return
	}
	c.Compare = checkComparison(resp.At, resp.Response)
	c.Compare.Path, _ = e.Args["path"].(string)
	c.Stages[stageArtifact], c.Stages[stageCompare] = stageOK, compareStage(c.Compare)
	switch {
	case c.Compare.Err != "":
		c.Stages[stageArtifact] = stageFailed
	case c.Compare.Match:
		d.matched = c
	}
}

// compareStage is a comparison's cell: its count of differing entries, or
// whether it matched.
func compareStage(c *Comparison) Stage {
	switch {
	case c.Err != "":
		return stageNone
	case c.Match:
		return stageMatch
	case c.Entries > 0:
		return Stage{"off", strconv.Itoa(c.Entries)}
	}
	return Stage{"off", "≠"}
}

// jobBuild opens a candidate for a build the job ran, unless it verifies the
// accepted candidate. later are the events after it.
func (d *deriver) jobBuild(e *TranscriptEvent, later []*TranscriptEvent, end time.Time) {
	b := e.Build
	from, to := buildSpan(b)
	if a := d.accepted; a != nil {
		outcome := buildOutcome(b.Steps)
		switch {
		case outcome == "succeeded":
			a.Stages[stageVerify] = stageOK
		case jobStages(b)[stageCompare].State == "off":
			a.Stages[stageVerify] = Stage{"off", "≠"}
		case outcome == "running":
			a.Stages[stageVerify] = Stage{"none", "…"}
		default:
			a.Stages[stageVerify] = stageFailed
		}
		d.add(e, Call{Tool: "job build", Cmd: b.ID, Result: outcome, Bad: outcome != "succeeded" && outcome != "running"})
		// The job attests after the build, so the session's remaining time is
		// the verification's.
		d.spans = append(d.spans, stretch{"verify", "verification build", from, end})
		d.accepted = nil
		return
	}
	d.flush()
	change := "Job build " + b.ID
	if p := d.proposals[b.Iter]; p != nil {
		change = "Job build of the " + p.Title
		if b.Iter == 1 {
			change = "Seed build from the " + p.Title
		}
	}
	c := d.open(e.At, change, b.ID)
	c.Job, c.Stages, c.took = true, jobStages(b), to.Sub(from)
	if st := c.Stages[stageCompare]; st.State == "off" || st.State == "match" {
		c.Compare = &Comparison{By: "job compare", At: to, Match: st.State == "match", forms: "builds/" + b.ID + "/"}
		// The job hands its diff summary to the next iteration: in the
		// iteration record, or in the prompt that opens the agent's turn.
		if r := d.records[b.Iter]; r != nil && r.Result != nil {
			c.Compare.Groups = parseDiffSummary(r.Result.ErrorMessage)
		}
		for _, p := range later {
			if c.Compare.Groups == nil && p.Kind == "prompt" && p.At.After(e.At) {
				c.Compare.Groups = parseDiffSummary(p.Body)
			}
		}
		c.Compare.Entries = entryCount(c.Compare.Groups)
		c.Stages[stageCompare] = compareStage(c.Compare)
	}
}

// review gives a review to the match it reviews, or else the current
// candidate.
func (d *deriver) review(e *TranscriptEvent) {
	c := cmp.Or(d.matched, d.cur)
	if c == nil {
		return
	}
	r := e.Review
	c.Review, c.ReviewAt = r, e.At
	result := r.Verdict
	switch {
	case r.Refused():
		rules := r.Rules()
		label := "✕"
		if len(rules) > 0 {
			label = rules[0]
			result = "refused: " + strings.Join(rules, ", ")
		}
		c.Stages[stageReview] = Stage{"failed", label}
		d.matched = nil
	case r.Verdict == "declare":
		c.Stages[stageReview] = Stage{"ok", "declare"}
		d.accepted = c
	default:
		c.Stages[stageReview] = stageOK
		d.accepted = c
	}
	d.add(e, Call{Tool: "review", Cmd: result, Bad: r.Refused(), Review: true})
}

// sessionVerdict sums up how the session ended over its candidates.
func sessionVerdict(s SessionView, cands []*Candidate, giveUp map[string]any, end time.Time) Verdict {
	v := Verdict{Kind: s.Verdict(), At: end, Summary: s.Summary}
	for _, c := range cands {
		if c.Compare != nil && c.Compare.Err == "" && !c.Compare.Match && (v.Closest == nil || c.Compare.Entries <= v.Closest.Compare.Entries) {
			v.Closest = c
		}
		if c.Compare != nil && c.Compare.Match {
			v.Matched = c
		}
		if c.Review != nil && !c.Review.Refused() {
			v.Accepted = c
		}
	}
	if v.Accepted != nil {
		v.Matched = nil
	}
	v.Remaining, _ = giveUp["remaining"].(string)
	if bc, ok := giveUp["build_commands"].(map[string]any); ok {
		deps, _ := bc["deps"].(string)
		build, _ := bc["build"].(string)
		v.Commands = strings.TrimSpace(deps + "\n" + build)
	}
	switch v.Kind {
	case "reproduced":
		v.Line = "The job reproduced the artifact"
		if v.Accepted != nil {
			v.Line = fmt.Sprintf("The job reproduced candidate %d", v.Accepted.N)
		}
	case "gave up":
		v.Line = s.Reason()
	case "running":
		v.Line = "Still running"
	default:
		v.Line, _, _ = strings.Cut(v.Summary, "\n")
	}
	return v
}

// humanDuration renders d in minutes and seconds: 5m 03s, 41s.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// stripKinds orders the strip's span kinds for its totals.
var stripKinds = []struct{ kind, label string }{
	{"timeout", "Timed out"},
	{"build", "Candidate builds"},
	{"verify", "Verification build"},
	{"setup", "Setup and seed"},
	{"other", "Other agent work"},
}

// layoutStrip places the spans on the session's time from start to end.
// Setup runs until the agent's first call.
func layoutStrip(spans []stretch, start, end, first time.Time) Strip {
	s := Strip{Start: start, End: end}
	total := end.Sub(start)
	if total <= 0 {
		return s
	}
	if first.IsZero() {
		first = end
	}
	spans = append([]stretch{{"setup", "setup and seed build", start, first}}, spans...)
	pct := func(d time.Duration) string { return strconv.FormatFloat(100*d.Seconds()/total.Seconds(), 'f', 2, 64) }
	sums := map[string]time.Duration{"other": total}
	for _, sp := range spans {
		if sp.from.Before(start) {
			sp.from = start
		}
		if sp.to.After(end) {
			sp.to = end
		}
		if !sp.to.After(sp.from) {
			continue
		}
		sums[sp.kind] += sp.to.Sub(sp.from)
		sums["other"] -= sp.to.Sub(sp.from)
		s.Spans = append(s.Spans, Span{Kind: sp.kind, Title: sp.title, Left: pct(sp.from.Sub(start)), Width: pct(sp.to.Sub(sp.from))})
	}
	for _, k := range stripKinds {
		if d := sums[k.kind]; d > 0 {
			s.Totals = append(s.Totals, Total{Kind: k.kind, Label: k.label, Took: humanDuration(d)})
		}
	}
	step := 2 * time.Minute
	for _, st := range []time.Duration{5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour} {
		if total/step <= 6 {
			break
		}
		step = st
	}
	for t := start.Truncate(step).Add(step); t.Before(end); t = t.Add(step) {
		s.Ticks = append(s.Ticks, Tick{Label: t.Format("15:04"), Left: pct(t.Sub(start))})
	}
	return s
}
