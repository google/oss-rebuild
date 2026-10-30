// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/buildpolicy"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// Free-form mode: the model iterates in the retained container, where trials
// are cheap, until check_artifact matches, then hands off its commands. The job
// formalizes them into candidates that each get one fresh build.
// The handoff and each candidate are judged by the content rule before a build.

const (
	formalizeRetries  = 2     // re-formalizations before proposing the handoff as written
	traceHead         = 1_000 // bytes kept from the head of a tool output in the formalization prompt
	traceTail         = 3_000 // bytes kept from its tail
	freeFormToolCalls = 300   // tool calls per turn
	handoffReminders  = 2     // follow-ups after a turn without a handoff, and after a refused one
	maxEndRefusals    = 2
)

// freeFormSession is a model session. Each turn (run, then followUp) returns
// its handoff, nil without one, and the session's tool calls so far.
type freeFormSession interface {
	run(ctx context.Context, prompt string) (handoff json.RawMessage, trace []toolCall, err error)
	followUp(ctx context.Context, prompt string) (handoff json.RawMessage, trace []toolCall, err error)
	close()
}

// toolCall is one entry of a session's trace.
type toolCall struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args,omitempty"`
	Output  string          `json:"output,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

// handoff is the handoff tool's arguments.
type handoff struct {
	Summary       string        `json:"summary"`
	BuildCommands buildCommands `json:"build_commands"`
}

// buildCommands are the commands that produced a candidate in the container,
// split into deps and build. They run after the attempt's deps phase.
type buildCommands struct {
	Deps  string `json:"deps"`
	Build string `json:"build"`
}

// freeForm is a pending handoff and its candidates, across Propose calls.
type freeForm struct {
	handoff    handoff
	trace      []toolCall
	exemplar   rebuild.Instructions // the attempt the model worked from
	allowed    *handoffVerdict      // the handoff's allowing review
	attempts   int                  // formalizations the model answered
	failures   []string             // each rejected candidate's reason
	fallback   bool                 // fallbackStrategy has been tried
	iterations int                  // len(iterHistory) at last proposal
}

// freeFormDriver runs the free-form mode over the agent's iterations.
type freeFormDriver struct {
	a        *defaultAgent
	runner   *ScratchRunner
	open     sessionOpener
	complete func(ctx context.Context, prompt string) (string, error)         // the formalization's model call
	store    func(ctx context.Context, opts *ProposeOpts, name string, v any) // uploadRecord, or a test's fake
	pending  *freeForm                                                        // the handoff being formalized
	checked  *schema.StrategyOneOf                                            // judged already, so check passes it
}

// sessionOpener starts a session over prev and returns its first prompt.
type sessionOpener func(ctx context.Context, opts *ProposeOpts, prev *executionDetails) (freeFormSession, string, error)

func newFreeFormDriver(a *defaultAgent, runner *ScratchRunner, open sessionOpener) *freeFormDriver {
	f := &freeFormDriver{a: a, runner: runner, open: open}
	f.complete = f.completeJSON
	f.store = a.uploadRecord
	return f
}

// propose continues the pending handoff or starts a session for a new one.
// A refused candidate costs no build, so the next is tried in the same call.
// A second handoff spent in one call is an error.
func (f *freeFormDriver) propose(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	if ff := f.pending; ff != nil {
		if n := len(f.a.iterHistory); n > ff.iterations {
			ff.failures = append(ff.failures, iterationFailure(f.a.iterHistory[n-1]))
		}
	}
	restarted := false
	for {
		if f.pending != nil {
			if s, err := f.nextCandidate(ctx, opts); s != nil || err != nil {
				return s, err
			}
			if restarted {
				return nil, errors.New("every candidate of the handoff was refused")
			}
			log.Printf("free-form: the handoff is spent, starting over")
			f.pending, restarted = nil, true
		}
		if err := f.handoffSession(ctx, opts); err != nil {
			return nil, err
		}
	}
}

// check is the agent's Check in free-form mode. It passes the candidate
// checkCandidate already judged.
func (f *freeFormDriver) check(ctx context.Context, opts *ProposeOpts, proposal *schema.StrategyOneOf) (string, error) {
	if proposal != nil && proposal == f.checked {
		return "", nil
	}
	return f.a.judgeProposal(ctx, opts, proposal)
}

// handoffSession runs a session to a handoff and makes it pending. A refused
// handoff continues the session from a tree reset to the ref.
func (f *freeFormDriver) handoffSession(ctx context.Context, opts *ProposeOpts) error {
	if err := f.runner.PrepareCompare(ctx); err != nil {
		return errors.Wrap(err, "staging the comparison inputs")
	}
	if len(f.a.iterHistory) == 0 {
		panic("free-form session without a previous iteration")
	}
	prev := f.a.execDetails(ctx, f.a.iterHistory[len(f.a.iterHistory)-1])
	sess, prompt, err := f.open(ctx, opts, prev)
	if err != nil {
		return err
	}
	defer sess.close()
	var h handoff
	var allowed *handoffVerdict
	var window int // trace index of the last restore
	judge := func(raw json.RawMessage, trace []toolCall) string {
		h, allowed = handoff{}, nil
		if json.Unmarshal(raw, &h) != nil || strings.TrimSpace(h.BuildCommands.Build) == "" {
			return ""
		}
		refusal, verdict, err := f.reviewHandoff(ctx, opts, h, trace[window:])
		if err != nil {
			log.Printf("free-form: reviewing the handoff: %v", err)
			return ""
		}
		if refusal == "" {
			allowed = verdict
			return ""
		}
		log.Printf("free-form: handoff refused")
		window = len(trace)
		f.restoreSource(ctx)
		return handoffRefusal(refusal)
	}
	raw, trace, err := sess.run(ctx, prompt)
	for reminders, refusals := 0, 0; err == nil; {
		var followUp string
		if raw == nil {
			if reminders++; reminders > handoffReminders {
				err = errors.New("the model ended without calling handoff")
				break
			}
			followUp = handoffReminder(trace)
		} else if followUp = judge(raw, trace); followUp != "" {
			if refusals++; refusals > maxEndRefusals {
				err = errors.Errorf("handoff refused %d times", refusals)
				break
			}
		}
		if followUp == "" {
			break
		}
		raw, trace, err = sess.followUp(ctx, followUp)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(h.BuildCommands.Build) == "" {
		return errors.New("handoff without build commands")
	}
	trace = trace[window:]
	f.a.reinferAfterMove(ctx, opts, &prev.Instructions)
	f.a.thoughts = append(f.a.thoughts, thoughtData{BasedOnIteration: len(f.a.iterHistory) - 1, Diagnostic: h.Summary, UpdatedScript: h.BuildCommands.Build})
	f.pending = &freeForm{handoff: h, trace: trace, exemplar: prev.Instructions, allowed: allowed}
	f.record(ctx, opts, stamp()+"-handoff.json", handoffRecord{Handoff: h, Exemplar: prev.Instructions, Trace: trace})
	return nil
}

// handoffReminder picks the reminder from the trace. A match stands until an
// accepted handoff spends it, even past a later mismatch.
func handoffReminder(trace []toolCall) string {
	for i := len(trace) - 1; i >= 0; i-- {
		switch c := trace[i]; {
		case c.Tool == "handoff" && !c.IsError:
			return handoffNoMatch
		case c.Tool == "handoff" && i == len(trace)-1:
			return handoffEarly
		case c.Tool == "check_artifact" && strings.HasPrefix(c.Output, "MATCH"):
			return handoffMatched
		}
	}
	return handoffNoMatch
}

// Wind-down and reminder texts.
const (
	keepIterating  = "Keep iterating in the container: run the build, call check_artifact on the result, and repeat until it reports a match, then call handoff with the commands that produced it. If the source at the ref cannot produce upstream's bytes, say so and stop."
	handoffMatched = "You ended without calling handoff after check_artifact reported a match. Call handoff now with the commands that produced the matching artifact: deps for every command that installed or fetched anything, build for the rest."
	handoffNoMatch = "You ended without calling handoff, and check_artifact has not reported a match in this session, so the work is not done. " + keepIterating
	handoffEarly   = "check_artifact has not reported a match in this session, so the handoff call was not accepted and the work is not done. " + keepIterating
)

func handoffRefusal(refusal string) string {
	return fmt.Sprintf("%s\n\nThe handoff is refused and was not formalized. Continue in this session: every byte of the artifact must come from the build tooling applied to the source at the ref. The container's source tree has been put back at the ref (git checkout --force, git clean -fd; ignored files such as installed dependencies remain), so redo what the build needs without the refused commands, rebuild the artifact from the source, call check_artifact again, and call handoff again with commands that contain none of them. If the release was cut from another commit, call set_source_ref with the commit and the evidence instead of checking it out in the handoff. If the source at the ref cannot produce upstream's bytes, say so and stop without calling handoff.", refusal)
}

// nextCandidate formalizes while the budget allows, then falls back to the
// handoff's commands as written. It returns nil once the handoff is spent.
// Refused candidates are skipped.
func (f *freeFormDriver) nextCandidate(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	ff := f.pending
	for ff.attempts < 1+formalizeRetries {
		if s, err := f.formalize(ctx, opts); s != nil || err != nil {
			return s, err
		}
	}
	if !ff.fallback {
		ff.fallback, ff.iterations = true, len(f.a.iterHistory)
		s := new(schema.NewStrategyOneOf(ff.fallbackStrategy(f.a.loc)))
		if f.checkCandidate(ctx, opts, s) == "" {
			log.Printf("free-form: %d formalizations rejected, proposing the handoff's commands as written", ff.attempts)
			return s, nil
		}
		log.Printf("free-form: the handoff's commands were refused as written")
	}
	return nil, nil
}

// handoffVerdict is the review that allowed a handoff. A candidate of the same
// commands carries it instead of a second review.
type handoffVerdict struct {
	CarriedFrom string              `json:"carried_from"`
	Review      *buildpolicy.Review `json:"review"`
}

// reviewHandoff judges a handoff with the session's commands and files the
// verdict. It returns an allow for the candidates to carry.
func (f *freeFormDriver) reviewHandoff(ctx context.Context, opts *ProposeOpts, h handoff, trace []toolCall) (refusal string, allowed *handoffVerdict, err error) {
	in := f.a.reviewInput(f.a.loc.Ref)
	in.Deps, in.Build, in.Transcript = h.BuildCommands.Deps, h.BuildCommands.Build, transcript(trace)
	var filed handoffVerdict
	refusal, err = f.a.judge(ctx, in, func(v *buildpolicy.Review) {
		filed = handoffVerdict{CarriedFrom: stamp() + "-handoff-review.json", Review: v}
		f.record(ctx, opts, filed.CarriedFrom, map[string]any{"handoff": h, "review": v})
	})
	if refusal != "" || err != nil || filed.CarriedFrom == "" {
		return refusal, nil, err
	}
	return "", &filed, nil
}

// carries reports whether inst runs exactly the handoff's commands on top of
// the exemplar's.
func (ff *freeForm) carries(inst rebuild.Instructions) bool {
	inherited := map[string]bool{}
	for _, l := range buildpolicy.Commands(ff.exemplar.Source + "\n" + ff.exemplar.Deps + "\n" + ff.exemplar.Build) {
		inherited[l] = true
	}
	own := func(script string) []string {
		var out []string
		for _, l := range buildpolicy.Commands(script) {
			if !inherited[l] {
				out = append(out, l)
			}
		}
		return out
	}
	h := ff.handoff.BuildCommands
	return slices.Equal(own(inst.Source+"\n"+inst.Deps+"\n"+inst.Build), own(h.Deps+"\n"+h.Build))
}

// checkCandidate judges a candidate as the fresh container would run it. One
// that carries the handoff's verdict is not reviewed again. A pass skips check.
func (f *freeFormDriver) checkCandidate(ctx context.Context, opts *ProposeOpts, s *schema.StrategyOneOf) string {
	st, err := s.Strategy()
	if err != nil {
		return ""
	}
	inst, err := st.GenerateFor(f.a.t, f.a.buildEnv())
	if err != nil {
		ms, ok := st.(*rebuild.ManualStrategy)
		if !ok {
			return ""
		}
		inst = rebuild.Instructions{Deps: ms.Deps, Build: ms.Build}
	}
	in := f.a.reviewInput(f.a.loc.Ref)
	in.Deps, in.Build = strings.TrimSpace(inst.Source+"\n"+inst.Deps), inst.Build
	record := func(v any) { f.record(ctx, opts, stamp()+"-review.json", v) }
	var refusal string
	if ff := f.pending; ff != nil && ff.allowed != nil && ff.carries(inst) {
		record(ff.allowed)
	} else if refusal, err = f.a.judge(ctx, in, func(v *buildpolicy.Review) { record(v) }); err != nil {
		log.Printf("free-form: reviewing the candidate: %v", err)
	}
	if refusal == "" {
		f.checked = s
	}
	return refusal
}

// restoreSource resets the container's tree to the ref. Ignored files, such as
// installed dependencies, stay.
func (f *freeFormDriver) restoreSource(ctx context.Context) {
	if !shellWord.MatchString(f.a.loc.Ref) {
		log.Printf("free-form: not restoring the source tree: ref %q", f.a.loc.Ref)
		return
	}
	script := dockerExecInContainerScript(fmt.Sprintf("cd /src && git checkout --force %s && git clean -fd", f.a.loc.Ref))
	if code, out, err := f.runner.RunCommand(ctx, script, 0); err != nil || code != 0 {
		log.Printf("free-form: restoring the source tree: exit %d, %v: %.400s", code, err, out)
	}
}

var (
	hostTreeRE     = regexp.MustCompile(`(?:/home/builder/|\./)?builds/[^/\s'"]+/src\b`)
	hostUpstreamRE = regexp.MustCompile(`(^|[\s"'=])(?:/home/builder/|\./)?upstream/`)
)

// hostPaths rewrites host paths of the build tree and upstream copy to the
// container's /src and /upstream, as the review reads them.
func hostPaths(cmd string) string {
	return hostUpstreamRE.ReplaceAllString(hostTreeRE.ReplaceAllString(cmd, "/src"), "${1}/upstream/")
}

// transcript renders the trace as the script the review reads: shell commands
// in container paths, reads as comments, no results.
func transcript(trace []toolCall) string {
	var b strings.Builder
	for _, c := range trace {
		var args struct{ Command, Path string }
		_ = json.Unmarshal(c.Args, &args)
		switch c.Tool {
		case "run_on_host", "run_in_container":
			b.WriteString(hostPaths(args.Command))
			b.WriteByte('\n')
		case "read_repo_file", "list_repo_files":
			fmt.Fprintf(&b, "# %s %s\n", c.Tool, args.Path)
		case "read_logs_end":
			fmt.Fprintf(&b, "# %s\n", c.Tool)
		}
	}
	return b.String()
}

// iterationFailure is what a rejected candidate's iteration reports.
func iterationFailure(it *schema.AgentIteration) string {
	if it.Result != nil && it.Result.ErrorMessage != "" {
		return it.Result.ErrorMessage
	}
	return "the build ended " + it.Status
}

// handoffRecord is the <stamp>-handoff.json transcript object.
type handoffRecord struct {
	Handoff  handoff              `json:"handoff"`
	Exemplar rebuild.Instructions `json:"exemplar"`
	Trace    []toolCall           `json:"trace"`
}

// formalizeRecord is the <stamp>-formalize-<n>.json transcript object.
type formalizeRecord struct {
	Attempt  int                   `json:"attempt"`
	Prompt   string                `json:"prompt"`
	Response string                `json:"response,omitempty"`
	Strategy *schema.StrategyOneOf `json:"strategy,omitempty"`
	Error    string                `json:"error,omitempty"`
}

// formalize asks the model for the handoff as a WorkflowStrategy. A failed
// call is an error, and a rejected or refused answer still spends an attempt.
func (f *freeFormDriver) formalize(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	ff := f.pending
	rec := formalizeRecord{Attempt: ff.attempts + 1, Prompt: formalizePrompt(f.a.t, ff)}
	name := fmt.Sprintf("%s-formalize-%d.json", stamp(), rec.Attempt)
	defer func() { f.record(ctx, opts, name, rec) }()
	var err error
	if rec.Response, err = f.complete(ctx, rec.Prompt); err != nil {
		err = errors.Wrap(err, "formalizing the handoff")
		rec.Error = err.Error()
		return nil, err
	}
	ff.attempts++
	reject := func(why string) {
		rec.Error = why
		ff.failures = append(ff.failures, why)
	}
	ws, err := decodeWorkflow(rec.Response, f.a.loc, ff.exemplar, f.a.t, f.a.buildEnv())
	if err != nil {
		reject("rejected before any build: " + err.Error())
		return nil, nil
	}
	ff.iterations = len(f.a.iterHistory)
	rec.Strategy = new(schema.NewStrategyOneOf(ws))
	if refusal := f.checkCandidate(ctx, opts, rec.Strategy); refusal != "" {
		reject("refused before any build: " + refusal)
		return nil, nil
	}
	return rec.Strategy, nil
}

// completeJSON asks the session model for a JSON answer in one call. Its usage
// counts as side usage.
func (f *freeFormDriver) completeJSON(ctx context.Context, prompt string) (string, error) {
	var out string
	err := f.a.deps.Retrier.Do(ctx, func() error {
		text, usage, err := llm.GenerateTextContentWithUsage(ctx, f.a.deps.GenaiClient, cmp.Or(f.a.deps.Model, llm.GeminiPro), &genai.GenerateContentConfig{
			Temperature:      genai.Ptr[float32](0),
			MaxOutputTokens:  65536,
			ResponseMIMEType: "application/json",
		}, genai.NewPartFromText(prompt))
		f.a.addSideUsage(usage)
		out = text
		return err
	})
	return out, err
}

// decodeWorkflow reads the formalization as a WorkflowStrategy on loc, with
// the exemplar's image and defaults, and checks that it renders.
func decodeWorkflow(out string, loc rebuild.Location, exemplar rebuild.Instructions, t rebuild.Target, be rebuild.BuildEnv) (*rebuild.WorkflowStrategy, error) {
	var ws rebuild.WorkflowStrategy
	if err := json.Unmarshal([]byte(stripFences(out)), &ws); err != nil {
		return nil, errors.Wrap(err, "decoding the strategy")
	}
	if len(ws.Build) == 0 {
		return nil, errors.New("the strategy has no build steps")
	}
	ws.Location = loc
	if len(ws.Source) == 0 {
		ws.Source = []flow.Step{{Uses: "git-checkout"}}
	}
	ws.OutputPath, ws.OutputDir = cmp.Or(ws.OutputPath, exemplar.OutputPath), ""
	ws.Requires.BaseImage, ws.Requires.Privileged = exemplar.Requires.BaseImage, exemplar.Requires.Privileged
	if ws.Requires.SystemDeps == nil {
		ws.Requires.SystemDeps = exemplar.Requires.SystemDeps
	}
	if _, err := ws.GenerateFor(t, be); err != nil {
		return nil, errors.Wrap(err, "rendering the strategy")
	}
	return &ws, nil
}

// fallbackStrategy is the handoff's commands as written on the exemplar's
// deps, for when no formalization verified.
func (ff *freeForm) fallbackStrategy(loc rebuild.Location) *rebuild.ManualStrategy {
	deps := ff.exemplar.Deps
	if d := strings.TrimSpace(ff.handoff.BuildCommands.Deps); d != "" {
		deps = strings.TrimRight(deps, "\n") + "\n" + d
	}
	return &rebuild.ManualStrategy{
		Location:   loc,
		Requires:   ff.exemplar.Requires,
		Deps:       deps,
		Build:      ff.handoff.BuildCommands.Build,
		OutputPath: ff.exemplar.OutputPath,
	}
}

// formalizeShape is the JSON the formalization asks for.
const formalizeShape = `{"deps": [{"runs": "<shell script>", "needs": ["<system package>"]}], "build": [{"runs": "<shell script>"}], "requires": {"system_deps": ["<system package>"]}, "output_path": "<path relative to /src>"}`

// formalizePrompt asks for ff as a WorkflowStrategy.
func formalizePrompt(t rebuild.Target, ff *freeForm) string {
	var b strings.Builder
	line := func(ss ...string) {
		for _, s := range ss {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	ex, h := ff.exemplar, ff.handoff
	line(
		fmt.Sprintf("You are writing oss-rebuild's build definition for %s package %s version %s (artifact %s) from a session in which a model built an artifact that matched upstream.", t.Ecosystem, t.Package, t.Version, t.Artifact),
		"The definition runs in a fresh container with the source checked out at /src: a deps phase with registry and network access, then a build phase without. Each phase is a list of steps, each step a shell script run in /src with errors fatal. The artifact must end up at output_path, relative to /src.",
		"",
		"# The attempt the model started from",
		"Its deps phase:", "```sh", strings.TrimRight(ex.Deps, "\n"), "```",
		"Its build phase:", "```sh", strings.TrimRight(ex.Build, "\n"), "```",
		"output_path: "+ex.OutputPath,
		"system_deps: "+strings.Join(ex.Requires.SystemDeps, ", "),
		"",
		"# What produced the matching artifact",
		"The model's summary: "+h.Summary,
		"Its deps commands, run after the attempt's deps phase:", "```sh", strings.TrimRight(h.BuildCommands.Deps, "\n"), "```",
		"Its build commands:", "```sh", strings.TrimRight(h.BuildCommands.Build, "\n"), "```",
		"",
		"# The session's tool calls, in order",
	)
	renderTrace(&b, ff.trace)
	if len(ff.failures) > 0 {
		line("", "# Rejected definitions", "Earlier definitions from this handoff were run in a fresh container and rejected. Fix what each rejection shows:")
		for i, f := range ff.failures {
			line(fmt.Sprintf("## Candidate %d", i+1), "```", f, "```")
		}
	}
	line(
		"",
		"# Output",
		"Reply with one JSON object and nothing else, of exactly this shape:",
		"```json", formalizeShape, "```",
		"Rules:",
		"- \"runs\" is a literal POSIX shell script. No template syntax, no markdown.",
		"- Keep the attempt's deps commands in their order and append the handoff's. Every command that installs or fetches anything, the registry pin included, goes in deps. The build phase has no network.",
		"- Do not clone or check out the source: the checkout is done before deps.",
		"- One step per logical unit (toolchain, registry pin, extra packages, the build), so a later reader sees the structure.",
		"- \"needs\" lists the system packages a step requires beyond the attempt's. \"system_deps\" is the complete list for the definition.",
		"- Use the trace to resolve what the handoff's commands assume: a working directory, a source edit, a file the model wrote. A source edit must become a command (sed, patch, a heredoc) in the build phase.",
	)
	return b.String()
}

func renderTrace(b *strings.Builder, trace []toolCall) {
	for i, c := range trace {
		fmt.Fprintf(b, "## %d. %s %s\n", i+1, c.Tool, c.argsLine())
		if c.IsError {
			b.WriteString("(error result)\n")
		}
		out, _ := truncateEnds(c.Output, traceHead, traceTail)
		fmt.Fprintf(b, "```\n%s\n```\n", strings.TrimRight(out, "\n"))
	}
}

func (c toolCall) argsLine() string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(c.Args, &args) == nil && args.Command != "" {
		return "$ " + args.Command
	}
	return string(c.Args)
}

// record files v in the iteration's transcript directory, best effort.
func (f *freeFormDriver) record(ctx context.Context, opts *ProposeOpts, name string, v any) {
	f.store(ctx, opts, name, v)
}

// stamp orders an iteration's transcript objects, as generate's does.
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// earlyHandoffResult answers a handoff before a match.
const earlyHandoffResult = "check_artifact has not reported a match in this session; keep working until it does, then call handoff"

// jobFreeFormGuidance ends a free-form session's first prompt.
func jobFreeFormGuidance() []string {
	return []string{
		"",
		"# This attempt",
		"Read the previous build's log with read_logs_end. Work in the container until check_artifact reports a match, then call handoff with the commands that produced it.",
	}
}

// freeFormPrompt is a free-form session's system prompt.
const freeFormPrompt = `You are an open source build expert. You work in the retained build container until you have built an artifact that matches the upstream one, then hand off the commands that produced it by calling handoff. The job turns the handoff into a build definition, runs it in a fresh container, and verifies the result itself. A build there is expensive and a trial in the container is cheap, so iterate in the container.

The build runs as two sequential phases on a fresh container: a deps phase with registry access, then a build phase. The source checkout, the toolchain, and the registry pin of the previous attempt are already in its container.

Where things are. run_on_host runs a shell command on the build VM host, in /home/builder, with busybox coreutils, git, docker and wget and no language runtimes. Each attempt is under builds/<build-id>/: build.sh is the script that ran, src/ the host side of the container's /src with whatever the build left there, and out/ the artifact it produced, if any. read_logs_end returns the tail of the previous build's log. The most recent build's container is retained as rb-<build-id>, stopped. run_in_container runs a command in it with the build's own toolchain. Each call starts in /src, which is builds/<build-id>/src on the host, so a file written in the container is readable on the host at that path, and nothing else in the container is. read_repo_file and list_repo_files read the pristine source at the ref. The upstream artifact is at upstream/<artifact> with its stabilized form beside it as <file>.stabilized, and the comparison tools are under tools/, host binaries with double-dash flags:

    tools/stabilize --ecosystem <ecosystem> --artifact <artifact> --infile <file> --outfile <file>.stabilized
    tools/diffr --summary --label candidate --label upstream <candidate>.stabilized <upstream>.stabilized
    tools/diffr <a>.stabilized <b>.stabilized

Checking a candidate. check_artifact compares a candidate against upstream the way the verification does: it stabilizes both with tools/stabilize, compares their sha256, and on a mismatch returns the head of diffr's file-level summary with the entry counts. Give it the candidate's host path, a file under builds/<build-id>/src/, or its container path under /src/, which it maps. For content hunks run tools/diffr on the stabilized forms it names.

How to work. Read the end of the build log and the mismatch summary first. Form a hypothesis, run the build commands in the container with run_in_container, check the result with check_artifact, and repeat until it matches. Keep tool output small with head, tail, grep, and wc rather than printing whole files or trees: a result keeps its first 4 KB and last 12 KB. Do not edit, repackage, or otherwise touch the produced artifact or the upstream artifact: the match must come from building the source. A build input that never ships may be changed by a command the handoff states (sed, patch, a heredoc), since a fresh container starts from the pristine checkout. Do not change the repository being built.

The content rule. Every byte of the artifact must be produced by the package's own build tooling from the source at the ref, or be absent. A match is necessary, not sufficient: the job reviews how the bytes were produced, in this session's commands and in the handoff, and refuses a handoff that supplied content instead of building it: writing literal content (a heredoc, printf, echo, tee, node -e, python -c, a typed hash, date, or version) into any file that ships in the artifact, lockfiles and package metadata included; copying or extracting anything from the published artifact, from upstream/, or from any commit, branch, or repository other than the ref; unpacking, editing, or re-creating the output archive with tar, zip, wheel, zipfile, or similar instead of letting the build tool produce it; editing the content of shipped source, metadata, or documentation files to match; deleting shipped files so the file set matches. Allowed, with a comment saying why: reproducing the publisher's environment (tool versions, environment variables, a build tool's configuration file wherever it lives, under node_modules or another dependency or tool directory included, removing .git so version tooling behaves as it did for the publisher, a value the publisher's tooling derives from the source such as gitHead from git rev-parse HEAD, a checkout with the line endings the publisher had); deleting files your build produces that upstream does not ship; editing build inputs that never ship; edits that leave the shipped bytes identical. Reading or extracting the upstream artifact to compare is fine; moving any of it into the source tree is not. If upstream contains content that cannot come from the source at the ref, such as a stray coverage or cache directory, editor swap files, or an untracked build product, say so and stop with the mismatch. A refused handoff comes back to you in this session with the refused commands; remove them, rebuild from the source, check again, and hand off again. A match produced by writing the differing bytes is a failure, not a success: an honest mismatch is the better outcome.

The source ref. If the evidence shows the release was cut from a different commit than the ref (the published gitHead or .cargo_vcs_info.json sha, a release tag, a version-bump commit), call set_source_ref with that commit and the evidence; the next attempt checks it out and re-infers the dependency phase there, and you may check it out in the container to continue. set_source_ref commits the session to the ref and re-infers the build; it is not a way to look at a ref. To find the commit a release was cut from, use git on the host in builds/<build-id>/src (git log, git diff <ref> -- <paths>, git show <ref>:<path>) against the stabilized upstream contents, then call set_source_ref once with the answer. A git checkout of another commit in the handoff's commands, or files taken from one, is refused.

When check_artifact reports a match, call handoff once with a short summary and build_commands: the minimal command sequence that produced the matching artifact in the container, as two scripts a fresh container runs in /src after the previous attempt's deps phase. deps holds every command that installs or fetches anything (packages, toolchain changes, registry access), build holds the rest, and each is complete on its own: a command the earlier attempt ran and the result still needs belongs in it too. Do not call handoff before a match has been reported.`
