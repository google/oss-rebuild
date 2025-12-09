// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundex

import (
	"context"
	"path"
	"slices"
	"sync"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// FirestoreClient is a wrapper around the external firestore client.
type FirestoreClient struct {
	client *firestore.Client
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
	return &FirestoreClient{client: client}, nil
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
			q := f.client.CollectionGroup("attempts").Query.Where("ecosystem", "==", batch.ecosystem).Where("package", "in", batch.packages)
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
				out <- newRebuildFromFirestore(doc)
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

	q := f.client.CollectionGroup("attempts").Query
	if req.Target != nil {
		t := *req.Target
		if t.Artifact == "" {
			if a, err := f.findArtifactName(ctx, t); err != nil {
				return nil, errors.Wrap(err, "inferring missing artifact")
			} else {
				t.Artifact = a
			}
		}
		et := rebuild.FirestoreTargetEncoding.Encode(t)
		q = f.client.Collection(path.Join("ecosystem", string(et.Ecosystem), "packages", et.Package, "versions", et.Version, "artifacts", et.Artifact, "attempts")).Query
	}
	if len(req.Executors) != 0 {
		q = q.Where("executor_version", "in", req.Executors)
	}
	if len(req.Runs) != 0 {
		q = q.Where("run_id", "in", req.Runs)
	}
	all := make(chan Rebuild)
	cerr := doQuery(ctx, q, newRebuildFromFirestore, all)
	rebuilds := filterRebuilds(all, req)
	if err := <-cerr; err != nil {
		return nil, errors.Wrap(err, "query error")
	}
	return rebuilds, nil
}

// FetchRuns fetches Runs out of firestore.
func (f *FirestoreClient) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	q := f.client.CollectionGroup("runs").Query
	if opts.BenchmarkHash != "" {
		q = q.Where("benchmark_hash", "==", opts.BenchmarkHash)
	}
	runs := make(chan Run)
	cerr := doQuery(ctx, q, newRunFromFirestore, runs)
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

// newRebuildFromFirestore creates a Rebuild instance from a "attempt" collection document.
func newRebuildFromFirestore(doc *firestore.DocumentSnapshot) Rebuild {
	var sa schema.RebuildAttempt
	if err := doc.DataTo(&sa); err != nil {
		panic(err)
	}
	var rb Rebuild
	rb.RebuildAttempt = sa
	return rb
}

// newRunFromFirestore creates a Run instance from a "runs" collection document.
func newRunFromFirestore(doc *firestore.DocumentSnapshot) Run {
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

// doQuery executes a query, transforming and sending each document to the output channel.
func doQuery[T any](ctx context.Context, q firestore.Query, fn func(*firestore.DocumentSnapshot) T, out chan<- T) <-chan error {
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

func (f *FirestoreClient) findArtifactName(ctx context.Context, t rebuild.Target) (string, error) {
	et := rebuild.FirestoreTargetEncoding.Encode(t)
	iter := f.client.Collection(path.Join("ecosystem", string(et.Ecosystem), "packages", et.Package, "versions", et.Version, "artifacts")).DocumentRefs(ctx)
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
	et.Artifact = artifacts[0]
	return et.Decode().Artifact, nil
}
