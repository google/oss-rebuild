// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rundex

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/tools/ctl/layout"
	"github.com/pkg/errors"
)

const (
	rebuildFileName = "firestore.json"
)

// FilesystemClient reads rebuilds from the local filesystem.
type FilesystemClient struct {
	fs                 billy.Filesystem
	runSubscribers     []chan<- *Run
	rebuildSubscribers []chan<- *Rebuild
}

var _ Reader = &FilesystemClient{}
var _ Watcher = &FilesystemClient{}
var _ Writer = &FilesystemClient{}

func NewFilesystemClient(fs billy.Filesystem) *FilesystemClient {
	return &FilesystemClient{
		fs: fs,
	}
}

// FetchRuns fetches Runs out of firestore.
func (f *FilesystemClient) FetchRuns(ctx context.Context, opts FetchRunsOpts) ([]Run, error) {
	runs := make([]Run, 0)
	err := util.Walk(f.fs, layout.RundexRunsDir, func(path string, info fs.FileInfo, err error) error {
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
func (f *FilesystemClient) FetchRebuilds(ctx context.Context, req *FetchRebuildRequest) ([]Rebuild, error) {
	walkErr := make(chan error, 1)
	all := make(chan Rebuild, 1)
	go func() {
		var toWalk []string
		if len(req.Runs) != 0 {
			for _, r := range req.Runs {
				toWalk = append(toWalk, filepath.Join(layout.RundexRebuildsDir, r))
			}
		} else {
			toWalk = []string{layout.RundexRebuildsDir}
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

func (f *FilesystemClient) WatchRuns() <-chan *Run {
	n := make(chan *Run, 1)
	f.runSubscribers = append(f.runSubscribers, n)
	return n
}

func (f *FilesystemClient) WatchRebuilds() <-chan *Rebuild {
	n := make(chan *Rebuild, 1)
	f.rebuildSubscribers = append(f.rebuildSubscribers, n)
	return n
}

func (f *FilesystemClient) WriteRebuild(ctx context.Context, r Rebuild) error {
	et := rebuild.FilesystemTargetEncoding.Encode(r.Target())
	path := filepath.Join(layout.RundexRebuildsDir, r.RunID, string(et.Ecosystem), et.Package, et.Version, et.Artifact, rebuildFileName)
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

func (f *FilesystemClient) WriteRun(ctx context.Context, r Run) error {
	path := filepath.Join(layout.RundexRunsDir, fmt.Sprintf("%s.json", r.ID))
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
