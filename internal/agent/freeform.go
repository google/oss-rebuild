// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

// Free-form mode: the model iterates in the retained container, where trials
// are cheap, until check_artifact matches, then hands off its commands. The job
// proposes them for one fresh build.

const (
	freeFormToolCalls = 300 // tool calls per turn
	handoffReminders  = 2   // follow-ups after a turn without a handoff
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

// freeForm is a pending handoff, across Propose calls.
type freeForm struct {
	handoff  handoff
	exemplar rebuild.Instructions // the attempt the model worked from
	proposed bool                 // its commands have been proposed
}

// freeFormDriver runs the free-form mode over the agent's iterations.
type freeFormDriver struct {
	a       *defaultAgent
	runner  *ScratchRunner
	open    sessionOpener
	store   func(ctx context.Context, opts *ProposeOpts, name string, v any) // uploadRecord, or a test's fake
	pending *freeForm                                                        // the handoff being proposed
}

// sessionOpener starts a session over prev and returns its first prompt.
type sessionOpener func(ctx context.Context, opts *ProposeOpts, prev *executionDetails) (freeFormSession, string, error)

func newFreeFormDriver(a *defaultAgent, runner *ScratchRunner, open sessionOpener) *freeFormDriver {
	f := &freeFormDriver{a: a, runner: runner, open: open}
	f.store = a.uploadRecord
	return f
}

// propose continues the pending handoff or starts a session for a new one.
func (f *freeFormDriver) propose(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	if f.pending != nil {
		if s, err := f.nextCandidate(ctx, opts); s != nil || err != nil {
			return s, err
		}
		log.Printf("free-form: the handoff is spent, starting over")
		f.pending = nil
	}
	if err := f.handoffSession(ctx, opts); err != nil {
		return nil, err
	}
	return f.nextCandidate(ctx, opts)
}

// handoffSession runs a session to a handoff and makes it pending.
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
	raw, trace, err := sess.run(ctx, prompt)
	for reminders := 0; err == nil && raw == nil; {
		if reminders++; reminders > handoffReminders {
			return errors.New("the model ended without calling handoff")
		}
		raw, trace, err = sess.followUp(ctx, handoffReminder(trace))
	}
	if err != nil {
		return err
	}
	var h handoff
	if err := json.Unmarshal(raw, &h); err != nil {
		return errors.Wrap(err, "decoding the handoff")
	}
	if strings.TrimSpace(h.BuildCommands.Build) == "" {
		return errors.New("handoff without build commands")
	}
	f.a.reinferAfterMove(ctx, opts, &prev.Instructions)
	f.a.thoughts = append(f.a.thoughts, thoughtData{BasedOnIteration: len(f.a.iterHistory) - 1, Diagnostic: h.Summary, UpdatedScript: h.BuildCommands.Build})
	f.pending = &freeForm{handoff: h, exemplar: prev.Instructions}
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

// nextCandidate proposes the handoff's commands once. It returns nil once the
// handoff is spent.
func (f *freeFormDriver) nextCandidate(ctx context.Context, opts *ProposeOpts) (*schema.StrategyOneOf, error) {
	ff := f.pending
	if ff.proposed {
		return nil, nil
	}
	ff.proposed = true
	return new(schema.NewStrategyOneOf(ff.fallbackStrategy(f.a.loc))), nil
}

// handoffRecord is the <stamp>-handoff.json transcript object.
type handoffRecord struct {
	Handoff  handoff              `json:"handoff"`
	Exemplar rebuild.Instructions `json:"exemplar"`
	Trace    []toolCall           `json:"trace"`
}

// fallbackStrategy is the handoff's commands as written on the exemplar's deps.
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
