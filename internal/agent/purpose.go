// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/internal/ratex"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/genai"
)

// Purposes: once a session has reported, one flash call classifies what each
// shell command the model ran was for, read back from the transcript the
// session wrote.

const (
	purposeTimeout        = 2 * time.Minute // bounds the whole classification, which the session never waits on
	purposeThinkingBudget = 256
	purposeCommandBytes   = 800 // of each command shown to the classifier
	purposeOutputBytes    = 160 // of each command's output
	purposeReadLimit      = 16  // transcript reads in flight
)

// purposePrompt is the classifier's instruction.
const purposePrompt = `Label each shell command an automated build agent ran while trying to reproduce a published package artifact from source. Give each command one primary purpose and the list of all purposes its parts serve, chosen from:
- inspect: observe only (ls, cat, head, grep, find, wc, which, --version, env, ps, git log/show/diff/status/tag, docker ps, listing or extracting an archive to read it, querying a registry, probing a port).
- compare: put the candidate and upstream side by side (tools/diffr, diff/cmp between candidate and upstream files or trees, both manifests, both checksums).
- source: move or clean the checkout (git clone/fetch/checkout/switch/reset/clean/stash/apply/restore, submodule update, removing .git).
- deps: install or fetch dependencies or toolchains (npm/yarn/pnpm install or ci, pip/gem/bundle install, cargo fetch, apk add, corepack enable, npm i -g <tool>, downloading a JDK or node tarball).
- build: compile, transpile, or generate toward the artifact without producing it (npm run build, tsc, lerna/nx run build, webpack, mvn compile, cargo build, make, sphinx-build, a codegen script).
- package: produce the artifact (npm/yarn/pnpm pack, python -m build, cargo package, gem build, rake gem, mvn package/install, jar, a release tool that packs).
- edit: change build inputs or files in the tree (sed -i, patch, writing or copying a file into the tree, npm pkg set, npm version, rm of tree files or prior outputs).
- setup: configure the environment outside the tree (export only, npm/git config, a settings.xml under ~/.m2, mkdir/ln -s outside the tree, starting or killing a service, removing toolchain shims).
A compound command's primary purpose is that of its most consequential part, by the precedence package > build > deps > source > edit > setup > compare > inspect, and its list names every purpose any part serves, the primary one included. Intent, not outcome: a failed npm pack is still package. Reading an extracted upstream copy alone is inspect; it is compare only when the candidate side is read or diffed in the same command.

Return a JSON array with one object per command, in order: {"i": <index>, "purpose": <label>, "all": [<label>, ...]}.`

// PurposeDeps wires the classifier.
type PurposeDeps struct {
	Client  *genai.Client
	GCS     *gcs.Client
	Bucket  string        // the sessions bucket
	Retrier ratex.Retrier // paces and retries the model call. Zero value calls once.
}

// ClassifyPurposes classifies the shell calls of the session's transcript: the
// model names the purpose of each run_on_host and run_in_container call, and
// check_artifact is the comparison it is. A call the model leaves out stays
// unclassified.
func ClassifyPurposes(ctx context.Context, deps PurposeDeps, sessionID string) (*schema.SessionPurposes, error) {
	calls, err := shellCalls(ctx, deps.GCS, deps.Bucket, sessionID)
	if err != nil {
		return nil, err
	}
	p := &schema.SessionPurposes{Model: llm.GeminiFlash, Calls: []schema.CallPurpose{}}
	purposes := make(map[int]schema.CallPurpose, len(calls)) // by index into calls
	var ask []int
	for i, c := range calls {
		if c.Tool == "check_artifact" {
			purposes[i] = schema.CallPurpose{Purpose: schema.PurposeCompare, All: []schema.Purpose{schema.PurposeCompare}}
		} else {
			ask = append(ask, i)
		}
	}
	if len(ask) > 0 {
		answers, usage, err := askPurposes(ctx, deps, calls, ask)
		if err != nil {
			return nil, err
		}
		p.Usage = usage.OrNil()
		for j, l := range answers {
			purposes[ask[j]] = l
		}
	}
	for i, c := range calls {
		if l, ok := purposes[i]; ok {
			l.Object, l.Call = c.Object, c.Call
			p.Calls = append(p.Calls, l)
		}
	}
	return p, nil
}

// StorePurposes writes p as the session's purposes object.
func StorePurposes(ctx context.Context, client *gcs.Client, bucket, sessionID string, p *schema.SessionPurposes) error {
	return uploadJSONRecord(ctx, client, bucket, schema.SessionPurposesObject(sessionID), p)
}

// askPurposes asks the model for the purposes of each of calls[ask[j]] in one
// request, with the calls numbered by j, and returns them by j.
func askPurposes(ctx context.Context, deps PurposeDeps, calls []transcriptCall, ask []int) (map[int]schema.CallPurpose, schema.TokenUsage, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%d commands from one session, in order.\n\n", len(ask))
	for j, i := range ask {
		calls[i].render(&b, j)
	}
	enum := make([]string, 0, len(schema.Purposes))
	for _, p := range schema.Purposes {
		enum = append(enum, string(p))
	}
	config := &genai.GenerateContentConfig{
		Temperature:       genai.Ptr[float32](0),
		SystemInstruction: genai.NewContentFromText(purposePrompt, genai.RoleUser),
		ThinkingConfig:    &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](purposeThinkingBudget)},
		ResponseMIMEType:  llm.JSONMIMEType,
		ResponseSchema: &genai.Schema{Type: genai.TypeArray, Items: &genai.Schema{
			Type:     genai.TypeObject,
			Required: []string{"i", "purpose", "all"},
			Properties: map[string]*genai.Schema{
				"i":       {Type: genai.TypeInteger},
				"purpose": {Type: genai.TypeString, Enum: enum},
				"all":     {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString, Enum: enum}},
			},
		}},
	}
	var items []struct {
		I       int              `json:"i"`
		Purpose schema.Purpose   `json:"purpose"`
		All     []schema.Purpose `json:"all"`
	}
	var usage schema.TokenUsage
	err := deps.Retrier.Do(ctx, func() error {
		u, err := llm.GenerateTypedContentWithUsage(ctx, deps.Client, llm.GeminiFlash, config, &items, genai.NewPartFromText(b.String()))
		usage = usage.Add(tokenUsageFromMetadata(u, llm.GeminiFlash))
		return err
	})
	if err != nil {
		return nil, usage, errors.Wrap(err, "classifying shell calls")
	}
	answers := make(map[int]schema.CallPurpose, len(items))
	for _, it := range items {
		if it.I >= 0 && it.I < len(ask) {
			answers[it.I] = schema.CallPurpose{Purpose: it.Purpose, All: allPurposes(it.Purpose, it.All)}
		}
	}
	return answers, usage, nil
}

// allPurposes orders all, with primary added, as schema.Purposes lists them.
// It drops repeats and anything the list does not name.
func allPurposes(primary schema.Purpose, all []schema.Purpose) []schema.Purpose {
	var out []schema.Purpose
	for _, p := range schema.Purposes {
		if p == primary || slices.Contains(all, p) {
			out = append(out, p)
		}
	}
	return out
}

// transcriptCall is one shell tool call of a transcript with its result.
type transcriptCall struct {
	Object  string // the transcript object, relative to messages/
	Call    int    // index among the object's function calls
	Tool    string
	Command string // the command, or check_artifact's path
	Exit    string // the exit code, or ? when the call got no response
	Output  string // the response's output
}

// render writes the call as the classifier sees it, numbered i.
func (c transcriptCall) render(b *strings.Builder, i int) {
	cmd := c.Command
	if len(cmd) > purposeCommandBytes {
		cmd = cmd[:purposeCommandBytes] + "\n...[truncated]"
	}
	fmt.Fprintf(b, "[%d] tool=%s exit=%s\n$ %s\n", i, c.Tool, c.Exit, cmd)
	if head := strings.TrimSpace(c.Output); head != "" {
		if len(head) > purposeOutputBytes {
			head = head[:purposeOutputBytes] + "..."
		}
		fmt.Fprintf(b, "> %s\n", strings.ReplaceAll(head, "\n", "\n> "))
	}
	b.WriteByte('\n')
}

// shellTools are the tools whose calls get a purpose.
var shellTools = map[string]bool{"run_on_host": true, "run_in_container": true, "check_artifact": true}

// messageObjectRE matches a chat message object's name relative to messages/,
// <iter>/<stamp>-<part>-<role>.json, as generate and chatSession.turn write
// them. Proposals and free-form records do not match.
var messageObjectRE = regexp.MustCompile(`^\d+/(.+)-(\d+)-(?:model|user)\.json$`)

// shellCalls reads the session's transcript in order and pairs each shell
// tool call with its response, which answers the earliest unanswered call of
// its tool.
func shellCalls(ctx context.Context, client *gcs.Client, bucket, sessionID string) ([]transcriptCall, error) {
	prefix := sessionID + "/messages/"
	type message struct {
		name string
		when time.Time
		part int
		body []byte
	}
	var msgs []message
	it := client.Bucket(bucket).Objects(ctx, &gcs.Query{Prefix: prefix})
	for attrs, err := range iterx.ToSeq2(it, iterator.Done) {
		if err != nil {
			return nil, errors.Wrap(err, "listing the transcript")
		}
		name := strings.TrimPrefix(attrs.Name, prefix)
		m := messageObjectRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		part, _ := strconv.Atoi(m[2])
		msgs = append(msgs, message{name: name, when: when, part: part})
	}
	// RFC3339Nano drops trailing zeros, so the names do not sort by time.
	sort.Slice(msgs, func(i, j int) bool {
		if !msgs[i].when.Equal(msgs[j].when) {
			return msgs[i].when.Before(msgs[j].when)
		}
		return msgs[i].part < msgs[j].part
	})
	var reads errgroup.Group
	reads.SetLimit(purposeReadLimit)
	for i := range msgs {
		reads.Go(func() error {
			r, err := client.Bucket(bucket).Object(prefix + msgs[i].name).NewReader(ctx)
			if err != nil {
				return errors.Wrapf(err, "reading %s", msgs[i].name)
			}
			defer r.Close()
			msgs[i].body, err = io.ReadAll(r)
			return errors.Wrapf(err, "reading %s", msgs[i].name)
		})
	}
	if err := reads.Wait(); err != nil {
		return nil, err
	}
	var calls []transcriptCall
	waiting := map[string][]int{} // unanswered calls by tool, as indices into calls
	for _, m := range msgs {
		var content genai.Content
		if err := json.Unmarshal(m.body, &content); err != nil {
			return nil, errors.Wrapf(err, "decoding %s", m.name)
		}
		n := 0
		for _, p := range content.Parts {
			switch {
			case p == nil:
			case p.FunctionCall != nil:
				if name := p.FunctionCall.Name; shellTools[name] {
					arg := "command"
					if name == "check_artifact" {
						arg = "path"
					}
					cmd, _ := p.FunctionCall.Args[arg].(string)
					waiting[name] = append(waiting[name], len(calls))
					calls = append(calls, transcriptCall{Object: m.name, Call: n, Tool: name, Command: cmd, Exit: "?"})
				}
				n++
			case p.FunctionResponse != nil:
				q := waiting[p.FunctionResponse.Name]
				if len(q) == 0 {
					continue
				}
				c := &calls[q[0]]
				waiting[p.FunctionResponse.Name] = q[1:]
				if code, ok := p.FunctionResponse.Response["exit_code"].(float64); ok {
					c.Exit = strconv.Itoa(int(code))
				}
				c.Output, _ = p.FunctionResponse.Response["output"].(string)
			}
		}
	}
	return calls, nil
}
