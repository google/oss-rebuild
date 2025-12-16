// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundex

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"slices"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/google/oss-rebuild/tools/ctl/pipe"
	"github.com/pkg/errors"
	"google.golang.org/api/iterator"
)

// GCSClient is a GCS-backed implementation of a rundex Reader.
type GCSClient struct {
	client *gcs.Client
	bucket string
	prefix string
}

// NewGCSClient creates a new GCSClient.
func NewGCSClient(ctx context.Context, client *gcs.Client, bucket, prefix string) *GCSClient {
	return &GCSClient{
		client: client,
		bucket: bucket,
		prefix: prefix,
	}
}

var _ Reader = &GCSClient{}
var _ Writer = &GCSClient{}

// FetchRuns fetches Runs out of GCS.
func (g *GCSClient) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	var runs []Run
	// TODO: If opts specifies specific runs, we can query for those directly.
	query := &gcs.Query{Prefix: path.Join(g.prefix, layout.RundexRunsPath) + "/"}
	it := g.client.Bucket(g.bucket).Objects(ctx, query)
	for attrs, err := range iterx.ToSeq2(it, iterator.Done) {
		if err != nil {
			return nil, errors.Wrap(err, "iterating over objects")
		}
		if !strings.HasSuffix(attrs.Name, ".json") {
			continue
		}
		obj := g.client.Bucket(g.bucket).Object(attrs.Name)
		r, err := obj.NewReader(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "creating reader for %s", attrs.Name)
		}
		var run Run
		if err := json.NewDecoder(r).Decode(&run); err != nil {
			r.Close()
			return nil, errors.Wrapf(err, "decoding run file %s", attrs.Name)
		}
		r.Close()
		if len(opts.IDs) != 0 && !slices.Contains(opts.IDs, run.ID) {
			continue
		}
		if opts.BenchmarkHash != "" && run.BenchmarkHash != opts.BenchmarkHash {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// FetchRebuilds fetches the Rebuild objects from GCS.
func (g *GCSClient) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) ([]Rebuild, error) {
	var prefixes []string
	if len(req.Runs) != 0 {
		for _, r := range req.Runs {
			prefixes = append(prefixes, path.Join(g.prefix, layout.RundexRebuildsPath, r)+"/")
		}
	} else {
		prefixes = append(prefixes, path.Join(g.prefix, layout.RundexRebuildsPath)+"/")
	}
	attrChan := make(chan *gcs.ObjectAttrs)
	errChan := make(chan error, 1)
	go func() {
		defer close(attrChan)
		for _, p := range prefixes {
			query := &gcs.Query{Prefix: p}
			it := g.client.Bucket(g.bucket).Objects(ctx, query)
			for attrs, err := range iterx.ToSeq2(it, iterator.Done) {
				if err != nil {
					errChan <- errors.Wrap(err, "iterating over objects")
					return
				}
				if filepath.Base(attrs.Name) != rebuildFileName {
					continue
				}
				attrChan <- attrs
			}
		}
		errChan <- nil
	}()
	// NOTE: This is a very large concurrency due to the large number of very small reads happening.
	// From local testing, this does not seems to run into issues.
	rebuildPipe := pipe.ParInto(1000, pipe.From(attrChan), func(attrs *gcs.ObjectAttrs, out chan<- Rebuild) {
		obj := g.client.Bucket(g.bucket).Object(attrs.Name)
		r, err := obj.NewReader(ctx)
		if err != nil {
			// TODO: Add error handling.
			return
		}
		defer r.Close()
		var rebuild Rebuild
		if err := json.NewDecoder(r).Decode(&rebuild); err != nil {
			// TODO: Add error handling.
			return
		}
		out <- rebuild
	})
	rebuilds := filterRebuilds(rebuildPipe.Out(), req)
	if err := <-errChan; err != nil {
		return nil, err
	}
	return rebuilds, nil
}

func (g *GCSClient) WriteRebuild(ctx context.Context, r Rebuild) error {
	et := rebuild.FilesystemTargetEncoding.Encode(r.Target())
	path := path.Join(g.prefix, layout.RundexRebuildsPath, r.RunID, string(et.Ecosystem), et.Package, et.Version, et.Artifact, rebuildFileName)
	obj := g.client.Bucket(g.bucket).Object(path)
	w := obj.NewWriter(ctx)
	if err := json.NewEncoder(w).Encode(r); err != nil {
		return errors.Wrap(err, "encoding rebuild")
	}
	if err := w.Close(); err != nil {
		return errors.Wrap(err, "closing writer")
	}
	return nil
}

func (g *GCSClient) WriteRun(ctx context.Context, r Run) error {
	path := path.Join(g.prefix, layout.RundexRunsPath, r.ID+".json")
	obj := g.client.Bucket(g.bucket).Object(path)
	w := obj.NewWriter(ctx)
	if err := json.NewEncoder(w).Encode(r); err != nil {
		return errors.Wrap(err, "encoding run")
	}
	if err := w.Close(); err != nil {
		return errors.Wrap(err, "closing writer")
	}
	return nil
}
