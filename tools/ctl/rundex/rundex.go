// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rundex provides access to metadata about runs and attempts.
package rundex

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
)

// Rebuild represents the result of a specific rebuild.
type Rebuild struct {
	schema.RebuildAttempt
}

func NewRebuildFromVerdict(v schema.Verdict, executor string, runID string, created time.Time) Rebuild {
	return Rebuild{
		RebuildAttempt: schema.RebuildAttempt{
			Ecosystem:       string(v.Target.Ecosystem),
			Package:         v.Target.Package,
			Version:         v.Target.Version,
			Artifact:        v.Target.Artifact,
			Success:         v.Message == "",
			Message:         v.Message,
			Strategy:        v.StrategyOneof,
			Timings:         v.Timings,
			ExecutorVersion: executor,
			RunID:           runID,
			Created:         created,
		},
	}
}

func (r Rebuild) Target() rebuild.Target {
	return rebuild.Target{
		Ecosystem: rebuild.Ecosystem(r.Ecosystem),
		Package:   r.Package,
		Version:   r.Version,
		Artifact:  r.Artifact,
	}
}

// ID returns a stable, human-readable formatting of the ecosystem, package, and version.
func (r *Rebuild) ID() string {
	return strings.Join([]string{r.Ecosystem, r.Package, r.Version, r.Artifact}, "!")
}

// WasSmoketest returns true if this rebuild was part of a smoketest run.
// NOTE: This will incorrectly appear to be Smoketest if an attestation failed during inference.
func (r Rebuild) WasSmoketest() bool {
	// TODO: Should we store the type of execution directly on the Rebuild? A more explicit check would involve looking up the Run object.
	return r.ObliviousID == ""
}

// Run represents a group of one or more rebuild executions.
type Run struct {
	schema.Run
	Type schema.ExecutionMode
}

func FromRun(r schema.Run) Run {
	var rb Run
	rb.Run = r
	rb.Type = schema.ExecutionMode(r.Type)
	return rb
}

type FetchRebuildOpts struct {
	Clean   bool
	Prefix  string
	Pattern string
}

// FetchRebuildRequest describes which Rebuild results you would like to fetch from firestore.
type FetchRebuildRequest struct {
	Target           *rebuild.Target
	Bench            *benchmark.PackageSet
	Executors        []string
	Runs             []string
	Opts             FetchRebuildOpts
	LatestPerPackage bool
}

// FetchRunsOpts describes which Runs you would like to fetch from firestore.
type FetchRunsOpts struct {
	IDs           []string
	BenchmarkHash string
}

type FetchSessionsReq struct {
	IDs           []string
	PartialTarget *rebuild.Target
	Since         time.Time
	Until         time.Time
	StopReason    string
}

type FetchIterationsReq struct {
	SessionID    string
	IterationIDs []string
}

type Reader interface {
	FetchRuns(context.Context, FetchRunsOpts) ([]Run, error)
	FetchRebuilds(context.Context, *FetchRebuildRequest) ([]Rebuild, error)
}

// TOOD: Move SessionReader into Reader when more impls exist.
type SessionReader interface {
	FetchSessions(context.Context, *FetchSessionsReq) ([]schema.AgentSession, error)
	FetchIterations(context.Context, *FetchIterationsReq) ([]schema.AgentIteration, error)
}

type Writer interface {
	WriteRebuild(ctx context.Context, r Rebuild) error
	WriteRun(ctx context.Context, r Run) error
}

// Watcher provides channels to notify about rundex object creation.
// The Watcher is expected to manage the lifecycle of the channels, closing them if necessary.
type Watcher interface {
	// TODO: We might want to add filter parameters similar to the Fecth* methods on Reader.
	WatchRuns() <-chan *Run
	WatchRebuilds() <-chan *Rebuild
}

func cleanVerdict(m string) string {
	// strip the leading error message of an inference failure to fit the latter, finer-grained error messages, on screen.
	m = strings.ReplaceAll(m, "getting strategy: fetching inference: failed to infer strategy:", "inference:")
	m = strings.ReplaceAll(m, "\n: 500 Internal Server Error: non-OK response", "")
	switch {
	// Generic
	case strings.Contains(m, "code = AlreadyExists desc = conflict with existing attestati"):
		m = "Success! (cached)"
	case strings.Contains(m, "executing rebuild: GCB build failed:"):
		m = "build: failed"
	case strings.Contains(m, "executing rebuild: GCB build internal error:"):
		m = "build: internal error"
	case strings.Contains(m, "code = FailedPrecondition desc = rebuild content mismatch"):
		m = "compare: content mismatch"
	case strings.Contains(m, "clone failed"):
		m = "repo: clone failed"
	case strings.HasPrefix(m, `mismatched version `):
		m = "wrong package version in manifest"
	case strings.HasPrefix(m, `mismatched name `):
		m = "wrong package name in manifest"
	case strings.Contains(m, `using existing: Failed to checkout: reference not found`):
		m = "unable to checkout main branch on reused repo"
	case strings.Contains(m, `unsupported repo type`):
		m = "repo: bad repo URL"
	case strings.HasPrefix(m, `Checkout failed`):
		m = "repo: git checkout failed"
	case strings.Contains(m, `Unsupported URL Type "workspace:"`):
		m = `npm install: unsupported scheme "workspace:"`
	case strings.Contains(m, `Unsupported URL Type "patch:"`):
		m = `npm install: unsupported scheme "patch:"`
	case strings.Contains(m, `getting strategy: fetching inference: making http request:`) && strings.Contains(m, `connection reset by peer`):
		m = `getting strategy: fetching inference: making http request to inference service: connection reset by peer`
	// NPM
	case strings.Contains(m, `npm is known not to run on Node.js`):
		m = "npm install: incompatible Node version"
	case strings.HasPrefix(m, "unknown npm pack failure:"):
		if strings.Contains(m, ": not found") {
			i := strings.Index(m, ": not found")
			cmd := m[strings.LastIndex(m[:i], " ")+1 : i]
			m = "missing build tool: " + cmd
		} else {
			m = "unknown pack failure"
		}
	case strings.HasPrefix(m, `Unsupported NPM version 'lerna/`):
		m = "missing pack tool: lerna"
	case strings.HasPrefix(m, "package.json file not found"):
		m = "manifest file not found"
	case strings.Contains(m, "files in the working directory contain changes"):
		m = "cargo failure: dirty working dir"
	case strings.Contains(m, `cloning repo: authentication require`):
		m = "authenticated repo"
	case strings.HasPrefix(m, "[INTERNAL] version heuristic checkout failed"):
		m = "version heuristic checkout failed"
	// PyPI
	case strings.Contains(m, `unsupported generator`):
		m = "unsupported generator: " + m[strings.LastIndex(m, ":")+3:len(m)-2]
	case strings.HasPrefix(m, `built version does not match requested version`):
		m = "built version does not match requested version"
	case strings.HasPrefix(strings.ToLower(m), "rebuild failure: repo invalid or private"):
		m = "repo invalid or private"
	case strings.HasPrefix(strings.ToLower(m), "rebuild failure: clone failed"):
		m = "clone failed"
	case strings.Contains(m, "Failed to extract upstream") && strings.Contains(m, ".dist-info/WHEEL: file does not exist"):
		m = "failed to extract upstream WHEEL"
	// Cargo
	case strings.HasPrefix(m, `Cargo.toml file not found`):
		m = "manifest file not found"
	case strings.HasPrefix(m, `[INTERNAL] Failed to find generated crate`):
		m = "missing generated crate"
	case strings.Contains(m, `Failed to request URL:`) && strings.Contains(m, `https://gateway`):
		m = "connection to gateway failed"
	case strings.Contains(m, `Failed to request URL:`) && strings.Contains(m, `crates.io`):
		m = "connection to crates.io failed"
	case strings.Contains(m, `believes it's in a workspace when it's not`):
		m = "cargo workspace error"
		// Maven
	case strings.HasPrefix(m, "inference: failed to resolve parent POM"):
		m = "inference: failed to resolve parent POM"
	case strings.HasPrefix(m, "inference: failed to find build.gradle directory"):
		m = "inference: failed to find build.gradle directory"
	case strings.Contains(m, "no download URL for JDK version"):
		m = `no download URL for JDK version`
	}
	return m
}

func filterRebuilds(all <-chan Rebuild, req *FetchRebuildRequest) []Rebuild {
	p := pipe.From(all)
	if req.Bench != nil {
		benchMap := make(map[string]benchmark.Package)
		for _, bp := range req.Bench.Packages {
			benchMap[bp.Name] = bp
		}
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			if bp, ok := benchMap[in.Package]; ok && slices.Contains(bp.Versions, in.Version) && bp.Ecosystem == in.Ecosystem {
				out <- in
			}
		})
	}
	if req.Opts.Prefix != "" {
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			if strings.HasPrefix(in.Message, req.Opts.Prefix) {
				out <- in
			}
		})
	}
	if req.Opts.Pattern != "" {
		pat := regexp.MustCompile(req.Opts.Pattern)
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			if pat.MatchString(in.Message) {
				out <- in
			}
		})
	}
	if req.Opts.Clean {
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			in.Message = cleanVerdict(in.Message)
			out <- in
		})
	}
	p = p.Do(func(in Rebuild, out chan<- Rebuild) {
		in.Message = strings.ReplaceAll(in.Message, "\n", "\\n")
		out <- in
	})
	var res []Rebuild
	if req.LatestPerPackage {
		rebuilds := make(map[string]Rebuild)
		for r := range p.Out() {
			if existing, seen := rebuilds[r.ID()]; seen && existing.Created.After(r.Created) {
				continue
			}
			rebuilds[r.ID()] = r
		}
		for _, r := range rebuilds {
			res = append(res, r)
		}
	} else {
		for r := range p.Out() {
			res = append(res, r)
		}
	}
	return res
}

// VerdictGroup is a collection of Rebuild objects, grouped by the same Message.
type VerdictGroup struct {
	Msg      string
	Count    int
	Examples []Rebuild
}

// GroupRebuilds will create VerdictGroup objects, grouping rebuilds by Message.
func GroupRebuilds(rebuilds []Rebuild) (byCount []*VerdictGroup) {
	msgs := make(map[string]*VerdictGroup)
	for _, r := range rebuilds {
		if _, seen := msgs[r.Message]; !seen {
			msgs[r.Message] = &VerdictGroup{Msg: r.Message}
		}
		msgs[r.Message].Count++
		msgs[r.Message].Examples = append(msgs[r.Message].Examples, r)
	}
	for _, vg := range msgs {
		slices.SortFunc(vg.Examples, func(a, b Rebuild) int {
			return strings.Compare(a.ID(), b.ID())
		})
		byCount = append(byCount, vg)
	}
	slices.SortFunc(byCount, func(a, b *VerdictGroup) int {
		return a.Count - b.Count
	})
	return
}
