// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package rundex provides access to metadata about runs and attempts.
package rundex

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// Rebuild represents the result of a specific rebuild.
type Rebuild struct {
	schema.RebuildAttempt
}

// NewRebuildFromFirestore creates a Rebuild instance from a "attempt" collection document.
func NewRebuildFromFirestore(doc *firestore.DocumentSnapshot) Rebuild {
	var sa schema.RebuildAttempt
	if err := doc.DataTo(&sa); err != nil {
		panic(err)
	}
	var rb Rebuild
	rb.RebuildAttempt = sa
	return rb
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

// NewRunFromFirestore creates a Run instance from a "runs" collection document.
func NewRunFromFirestore(doc *firestore.DocumentSnapshot) Run {
	var r schema.Run
	if err := doc.DataTo(&r); err != nil {
		panic(err)
	}
	// Historical, past entries only contain runid in the doc.Ref.ID, not inside the document.
	if r.ID == "" {
		r.ID = doc.Ref.ID
	}
	return FromRun(r)
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

type Reader interface {
	FetchRuns(context.Context, FetchRunsOpts) ([]Run, error)
	FetchRebuilds(context.Context, *FetchRebuildRequest) ([]Rebuild, error)
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

// FirestoreClient is a wrapper around the external firestore client.
type FirestoreClient struct {
	Client *firestore.Client
}

// FirestoreClient is only a Reader for now.
var _ Reader = &FirestoreClient{}

// NewFirestore creates a new FirestoreClient.
func NewFirestore(ctx context.Context, project string) (*FirestoreClient, error) {
	if project == "" {
		return nil, errors.New("empty project provided")
	}
	client, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	return &FirestoreClient{Client: client}, nil
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

func sanitize(key string) string {
	return strings.ReplaceAll(key, "/", "!")
}

func (f *FirestoreClient) findArtifactName(ctx context.Context, t rebuild.Target) (string, error) {
	iter := f.Client.Collection(path.Join("ecosystem", string(t.Ecosystem), "packages", sanitize(t.Package), "versions", t.Version, "artifacts")).DocumentRefs(ctx)
	var artifacts []string
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return "", err
		}
		artifacts = append(artifacts, doc.ID)
	}
	if len(artifacts) == 0 {
		return "", errors.New("no artifact documents found")
	}
	if len(artifacts) > 1 {
		return "", errors.New("multiple artifact documents found")
	}
	return artifacts[0], nil
}

// FetchRebuilds fetches the Rebuild objects out of firestore.
func (f *FirestoreClient) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) ([]Rebuild, error) {
	if len(req.Executors) != 0 && len(req.Runs) != 0 {
		return nil, errors.New("only provide one of executors and runs")
	}
	if req.Bench != nil && req.Bench.Count == 0 {
		return nil, errors.New("empty bench provided")
	}
	// If a benchmark is provided, we can optimize by querying for specific packages.
	if req.Bench != nil {
		packagesByEcosystem := make(map[string][]string)
		for _, p := range req.Bench.Packages {
			if !slices.Contains(packagesByEcosystem[p.Ecosystem], p.Name) {
				packagesByEcosystem[p.Ecosystem] = append(packagesByEcosystem[p.Ecosystem], p.Name)
			}
		}

		type queryBatch struct {
			ecosystem string
			packages  []string
		}
		var batches []queryBatch
		const batchSize = 30 // Firestore 'in' queries are limited to 30 values.
		for eco, pkgs := range packagesByEcosystem {
			for i := 0; i < len(pkgs); i += batchSize {
				end := i + batchSize
				if end > len(pkgs) {
					end = len(pkgs)
				}
				batches = append(batches, queryBatch{ecosystem: eco, packages: pkgs[i:end]})
			}
		}

		p := pipe.FromSlice(batches)

		var queryErr error
		var once sync.Once
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()

		const queryConcurrency = 10
		rebuildsPipe := pipe.ParInto(queryConcurrency, p, func(batch queryBatch, out chan<- Rebuild) {
			if pctx.Err() != nil {
				return
			}
			q := f.Client.CollectionGroup("attempts").Query.Where("ecosystem", "==", batch.ecosystem).Where("package", "in", batch.packages)
			if len(req.Executors) != 0 {
				q = q.Where("executor_version", "in", req.Executors)
			}
			if len(req.Runs) != 0 {
				q = q.Where("run_id", "in", req.Runs)
			}
			iter := q.Documents(pctx)
			for {
				doc, err := iter.Next()
				if err == iterator.Done {
					break
				}
				if err != nil {
					once.Do(func() {
						queryErr = errors.Wrap(err, "query error")
						cancel()
					})
					break
				}
				out <- NewRebuildFromFirestore(doc)
			}
		})
		var allUnfilteredRebuilds []Rebuild
		for r := range rebuildsPipe.Out() {
			allUnfilteredRebuilds = append(allUnfilteredRebuilds, r)
		}
		if queryErr != nil {
			return nil, queryErr
		}
		// Now filter all results at once.
		allChan := make(chan Rebuild, len(allUnfilteredRebuilds))
		for _, r := range allUnfilteredRebuilds {
			allChan <- r
		}
		close(allChan)
		return filterRebuilds(allChan, req), nil
	}

	q := f.Client.CollectionGroup("attempts").Query
	if req.Target != nil {
		t := *req.Target
		if t.Artifact == "" {
			if a, err := f.findArtifactName(ctx, t); err != nil {
				return nil, errors.Wrap(err, "inferring missing artifact")
			} else {
				t.Artifact = a
			}
		}
		q = f.Client.Collection(path.Join("ecosystem", string(t.Ecosystem), "packages", sanitize(t.Package), "versions", t.Version, "artifacts", t.Artifact, "attempts")).Query
	}
	if len(req.Executors) != 0 {
		q = q.Where("executor_version", "in", req.Executors)
	}
	if len(req.Runs) != 0 {
		q = q.Where("run_id", "in", req.Runs)
	}
	all := make(chan Rebuild)
	cerr := DoQuery(ctx, q, NewRebuildFromFirestore, all)
	rebuilds := filterRebuilds(all, req)
	if err := <-cerr; err != nil {
		return nil, errors.Wrap(err, "query error")
	}
	return rebuilds, nil
}

// FetchRuns fetches Runs out of firestore.
func (f *FirestoreClient) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	q := f.Client.CollectionGroup("runs").Query
	if opts.BenchmarkHash != "" {
		q = q.Where("benchmark_hash", "==", opts.BenchmarkHash)
	}
	runs := make(chan Run)
	cerr := DoQuery(ctx, q, NewRunFromFirestore, runs)
	var runSlice []Run
	for r := range runs {
		if len(opts.IDs) != 0 && !slices.Contains(opts.IDs, r.ID) {
			continue
		}
		runSlice = append(runSlice, r)
	}
	if err := <-cerr; err != nil {
		return nil, errors.New("query error")
	}
	return runSlice, nil
}

// LocalClient reads rebuilds from the local filesystem.
type LocalClient struct {
	fs                 billy.Filesystem
	runSubscribers     []chan<- *Run
	rebuildSubscribers []chan<- *Rebuild
}

var _ Reader = &LocalClient{}
var _ Watcher = &LocalClient{}
var _ Writer = &LocalClient{}

func NewLocalClient(fs billy.Filesystem) *LocalClient {
	return &LocalClient{
		fs: fs,
	}
}

const (
	rebuildFileName = "firestore.json"
)

// FetchRuns fetches Runs out of firestore.
func (f *LocalClient) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	runs := make([]Run, 0)
	err := util.Walk(f.fs, layout.RundexRunsPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		file, err := f.fs.Open(path)
		if err != nil {
			return errors.Wrap(err, "opening run file")
		}
		defer file.Close()
		var r Run
		if err := json.NewDecoder(file).Decode(&r); err != nil {
			return errors.Wrap(err, "decoding run file")
		}
		if len(opts.IDs) != 0 && !slices.Contains(opts.IDs, r.ID) {
			return nil
		}
		if opts.BenchmarkHash != "" && r.BenchmarkHash != opts.BenchmarkHash {
			return nil
		}
		runs = append(runs, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

// FetchRebuilds fetches the Rebuild objects from local paths.
func (f *LocalClient) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) ([]Rebuild, error) {
	walkErr := make(chan error, 1)
	all := make(chan Rebuild, 1)
	go func() {
		var toWalk []string
		if len(req.Runs) != 0 {
			for _, r := range req.Runs {
				toWalk = append(toWalk, filepath.Join(layout.RundexRebuildsPath, r))
			}
		} else {
			toWalk = []string{layout.RundexRebuildsPath}
		}
		defer close(all)
		for _, p := range toWalk {
			err := util.Walk(f.fs, p, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if filepath.Base(path) != rebuildFileName {
					return nil
				}
				file, err := f.fs.Open(path)
				if err != nil {
					return errors.Wrap(err, "opening firestore file")
				}
				defer file.Close()
				var r Rebuild
				if err := json.NewDecoder(file).Decode(&r); err != nil {
					return errors.Wrap(err, "decoding firestore file")
				}
				all <- r
				return nil
			})
			if err != nil {
				walkErr <- err
				return
			}
		}
		walkErr <- nil
	}()
	rebuilds := filterRebuilds(all, req)
	if err := <-walkErr; err != nil {
		return nil, errors.Wrap(err, "exploring rebuilds dir")
	}
	return rebuilds, nil
}

func (f *LocalClient) WatchRuns() <-chan *Run {
	n := make(chan *Run, 1)
	f.runSubscribers = append(f.runSubscribers, n)
	return n
}

func (f *LocalClient) WatchRebuilds() <-chan *Rebuild {
	n := make(chan *Rebuild, 1)
	f.rebuildSubscribers = append(f.rebuildSubscribers, n)
	return n
}

func (f *LocalClient) WriteRebuild(ctx context.Context, r Rebuild) error {
	path := filepath.Join(layout.RundexRebuildsPath, r.Ecosystem, r.Package, r.Artifact, rebuildFileName)
	file, err := f.fs.Create(path)
	if err != nil {
		return errors.Wrap(err, "creating file")
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(r); err != nil {
		return err
	}
	for _, sub := range f.rebuildSubscribers {
		go func() {
			sub <- &r
		}()
	}
	return nil
}

func (f *LocalClient) WriteRun(ctx context.Context, r Run) error {
	path := filepath.Join(layout.RundexRunsPath, fmt.Sprintf("%s.json", r.ID))
	file, err := f.fs.Create(path)
	if err != nil {
		return errors.Wrap(err, "creating file")
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(r); err != nil {
		return err
	}
	for _, sub := range f.runSubscribers {
		go func() {
			sub <- &r
		}()
	}
	return nil
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
