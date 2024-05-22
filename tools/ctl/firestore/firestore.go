// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package firestore helps interact with the rebuild results stored in firestore.
package firestore

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"cloud.google.com/go/firestore"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// Rebuild represents the result of a specific rebuild.
type Rebuild struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	Success   bool
	Message   string
	Strategy  string
	Executor  string
	Run       string
	Created   time.Time
	Timings   rebuild.Timings
}

// NewRebuildFromFirestore creates a Rebuild instance from a "attempt" collection document.
func NewRebuildFromFirestore(doc *firestore.DocumentSnapshot) Rebuild {
	var rb Rebuild
	// Because things have been added incrementally, we treat everything as optional. Too many times
	// has this been the cause of a nil dereference and panic.
	//
	// TODO: use firestore's DataTo method with a struct defined in schema.go
	// https://pkg.go.dev/cloud.google.com/go/firestore#DocumentSnapshot.DataTo
	if d, ok := doc.Data()["ecosystem"]; ok {
		rb.Ecosystem = d.(string)
	}
	if d, ok := doc.Data()["package"]; ok {
		rb.Package = d.(string)
	}
	if d, ok := doc.Data()["version"]; ok {
		rb.Version = d.(string)
	}
	if d, ok := doc.Data()["success"]; ok {
		rb.Success = d.(bool)
	}
	if d, ok := doc.Data()["message"]; ok {
		rb.Message = d.(string)
	}
	if d, ok := doc.Data()["strategy"]; ok {
		rb.Strategy = d.(string)
	}
	if d, ok := doc.Data()["executor_version"]; ok {
		rb.Executor = d.(string)
	}
	if d, ok := doc.Data()["run_id"]; ok {
		rb.Run = d.(string)
	}
	if d, ok := doc.Data()["doc"]; ok {
		rb.Created = time.UnixMilli(d.(int64))
	}
	if d, ok := doc.Data()["artifact"]; ok {
		rb.Artifact = d.(string)
	}
	if d, ok := doc.Data()["time_clone_estimate"]; ok {
		rb.Timings.CloneEstimate = time.Duration(d.(float64) * float64(time.Second))
	}
	if d, ok := doc.Data()["time_source"]; ok {
		rb.Timings.Source = time.Duration(d.(float64) * float64(time.Second))
	}
	if d, ok := doc.Data()["time_infer"]; ok {
		rb.Timings.Infer = time.Duration(d.(float64) * float64(time.Second))
	}
	if d, ok := doc.Data()["time_build"]; ok {
		rb.Timings.Build = time.Duration(d.(float64) * float64(time.Second))
	}
	return rb
}

type BenchmarkMode string

const (
	SmoketestMode BenchmarkMode = "smoketest"
	AttestMode    BenchmarkMode = "attest"
)

// Run represents a group of one or more rebuild executions.
type Run struct {
	ID            string
	BenchmarkName string
	BenchmarkHash string
	Type          BenchmarkMode
	Created       time.Time
}

// NewRunFromFirestore creates a Run instance from a "runs" collection document.
func NewRunFromFirestore(doc *firestore.DocumentSnapshot) Run {
	var typ BenchmarkMode
	if maybeType, ok := doc.Data()["run_type"]; ok {
		typ = BenchmarkMode(maybeType.(string))
	}
	return Run{
		ID:            doc.Ref.ID,
		BenchmarkName: doc.Data()["benchmark_name"].(string),
		BenchmarkHash: doc.Data()["benchmark_hash"].(string),
		Type:          typ,
		Created:       time.UnixMilli(doc.Data()["created"].(int64)),
	}
}

// ID returns a stable, human-readable formatting of the ecosystem, package, and version.
func (r *Rebuild) ID() string {
	return strings.Join([]string{r.Ecosystem, r.Package, r.Version}, "!")
}

// DoQuery executes a query, transforming and sending each document to the output channel.
func DoQuery[T any](ctx context.Context, q firestore.Query, fn func(*firestore.DocumentSnapshot) T, out chan<- T) <-chan error {
	ret := make(chan error, 1)
	iter := q.Documents(ctx)
	go func() {
		defer close(ret)
		defer close(out)
		for {
			doc, err := iter.Next()
			if err == iterator.Done {
				ret <- nil
				break
			}
			if err != nil {
				ret <- err
				break
			}
			out <- fn(doc)
		}
	}()
	return ret
}

func cleanVerdict(m string) string {
	switch {
	// Generic
	case strings.HasPrefix(m, `mismatched version `):
		m = "wrong package version in manifest"
	case strings.HasPrefix(m, `mismatched name `):
		m = "wrong package name in manifest"
	case strings.Contains(m, `using existing: Failed to checkout: reference not found`):
		m = "unable to checkout main branch on reused repo"
	case strings.HasPrefix(m, `Unknown repo URL type:`):
		m = "bad repo URL"
	case strings.Contains(m, `npm is known not to run on Node.js`):
		m = "npm install: incompatible Node version"
	case strings.Contains(m, `Unsupported URL Type "workspace:"`):
		m = `npm install: unsupported scheme "workspace:"`
	case strings.Contains(m, `Unsupported URL Type "patch:"`):
		m = `npm install: unsupported scheme "patch:"`
	// NPM
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
	case strings.HasPrefix(m, `rebuild failure: rebuilt artifact not found upstream: `):
		m = "rebuilt artifact not found upstream"
	case strings.HasPrefix(m, "rebuild failure: Clone failed"):
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
	case strings.HasPrefix(m, `Checkout failed`):
		m = "git checkout failed"
	}
	return m
}

// Client is a wrapper around the external firestore client.
type Client struct {
	Client *firestore.Client
}

// NewClient creates a new FirestoreClient.
func NewClient(ctx context.Context, project string) (*Client, error) {
	if project == "" {
		return nil, errors.New("empty project provided")
	}
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &Client{Client: client}, nil
}

type FetchRebuildOpts struct {
	Clean  bool
	Filter string
}

// FetchRebuildRequest describes which Rebuild results you would like to fetch from firestore.
type FetchRebuildRequest struct {
	Bench     *benchmark.PackageSet
	Executors []string
	Runs      []string
	Opts      FetchRebuildOpts
}

// FetchRebuilds fetches the Rebuild objects out of firestore.
func (f *Client) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) (rebuilds map[string]Rebuild, err error) {
	log.Println("Analyzing results...")
	if len(req.Executors) != 0 && len(req.Runs) != 0 {
		return nil, errors.New("only provide one of executors and runs")
	}
	if req.Bench != nil && req.Bench.Count == 0 {
		return nil, errors.New("empty bench provided")
	}
	q := f.Client.CollectionGroup("attempts").Query
	if len(req.Executors) != 0 {
		log.Printf("Searching rebuild results for executor versions '%v'...\n", req.Executors)
		q = q.Where("executor_version", "in", req.Executors)
	}
	if len(req.Runs) != 0 {
		log.Printf("Searching rebuild results for runs '%v'...\n", req.Runs)
		q = q.Where("run_id", "in", req.Runs)
	}
	all := make(chan Rebuild)
	cerr := DoQuery(ctx, q, NewRebuildFromFirestore, all)
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
	if req.Opts.Filter != "" {
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			if strings.HasPrefix(in.Message, req.Opts.Filter) {
				out <- in
			}
		})
	}
	// Post-processing
	p = p.Do(func(in Rebuild, out chan<- Rebuild) {
		if strings.HasPrefix(in.Message, `rebuild failure: rebuilt artifact not found upstream: `) {
			artifact := strings.TrimPrefix(in.Message, `rebuild failure: rebuilt artifact not found upstream: `)
			builtVersion := strings.Split(artifact, "-")[1]
			if builtVersion != in.Version {
				in.Message = fmt.Sprintf("built version does not match requested version (%s vs %s)", builtVersion, in.Version)
			}
		}
		out <- in
	})
	if req.Opts.Clean {
		p = p.Do(func(in Rebuild, out chan<- Rebuild) {
			in.Message = cleanVerdict(in.Message)
			out <- in
		})
	}
	rebuilds = make(map[string]Rebuild)
	for r := range p.Out() {
		if existing, seen := rebuilds[r.ID()]; seen && existing.Created.After(r.Created) {
			continue
		}
		r.Message = strings.ReplaceAll(r.Message, "\n", "\\n")
		rebuilds[r.ID()] = r
	}
	if err := <-cerr; err != nil {
		log.Fatal("query error", err.Error())
	}
	return
}

// FetchRunsOpts  describes which Runs you would like to fetch from firestore.
type FetchRunsOpts struct {
	BenchmarkHash string
}

// FetchRuns fetches Runs out of firestore.
func (f *Client) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	q := f.Client.CollectionGroup("runs").Query
	if opts.BenchmarkHash != "" {
		q = q.Where("benchmark_hash", "==", opts.BenchmarkHash)
	}
	runs := make(chan Run)
	cerr := DoQuery(ctx, q, NewRunFromFirestore, runs)
	runSlice := make([]Run, 0, 0)
	for r := range runs {
		runSlice = append(runSlice, r)
	}
	if err := <-cerr; err != nil {
		return nil, errors.New("query error")
	}
	return runSlice, nil
}

// VerdictGroup is a collection of Rebuild objects, grouped by the same Message.
type VerdictGroup struct {
	Msg      string
	Count    int
	Examples []Rebuild
}

// GroupRebuilds will create VerdictGroup objects, grouping rebuilds by Message.
func GroupRebuilds(rebuilds map[string]Rebuild) (byCount []*VerdictGroup) {
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
