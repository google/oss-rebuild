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

// Package main implements a repo scanning tool to identify the best ref match for an upstream artifact.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/bitmap"
	mavenreg "github.com/google/oss-rebuild/pkg/registry/maven"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
)

var (
	ecosystem = flag.String("ecosystem", "maven", "package ecosystem")
	pkg       = flag.String("pkg", "", "package identifier")
	version   = flag.String("version", "", "package version")
	repo      = flag.String("repo", "", "package repo")
	repoPath  = flag.String("repo-path", "", "local path from which to load the package repo")
	strategy  = flag.String("strategy", "dynamic", "strategy to use to search and rank commits. {dynamic, commits-near-publish}")
)

func getRepo(ctx context.Context, uri, path string) (*git.Repository, error) {
	var s storage.Storer
	if *repoPath != "" {
		gitfs, err := osfs.New(*repoPath).Chroot(".git")
		if err != nil {
			return nil, errors.Wrap(err, "creating git chroot")
		}
		s = filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: true})
	} else {
		base, err := os.MkdirTemp("", "oss-rebuild")
		if err != nil {
			return nil, errors.Wrap(err, "creating temp dir")
		}
		if err := os.Mkdir(filepath.Join(base, ".git"), os.ModePerm); err != nil {
			return nil, errors.Wrap(err, "creating temp dir")
		}
		gitfs, err := osfs.New(base).Chroot(".git")
		if err != nil {
			return nil, errors.Wrap(err, "creating git chroot")
		}
		s = filesystem.NewStorageWithOptions(gitfs, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: true})
	}
	if path != "" {
		r, err := git.Open(s, nil)
		return r, errors.Wrap(err, "opening repo")
	}
	r, err := git.CloneContext(ctx, s, nil, &git.CloneOptions{URL: uri, NoCheckout: true})
	return r, errors.Wrap(err, "cloning repo")
}

func zipHashes(zr *zip.Reader) (files []string, err error) {
	var f io.ReadCloser
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		size := int64(zf.UncompressedSize64)
		if size < 0 {
			// TODO: support git LFS?
			err = errors.Errorf("file exceeds max supported size: %d", zf.UncompressedSize64)
			return
		}
		h := plumbing.NewHasher(plumbing.BlobObject, size)
		f, err = zf.Open()
		if err != nil {
			return
		}
		if _, err = io.CopyN(h, f, size); err != nil {
			return
		}
		if err = f.Close(); err != nil {
			return
		}
		files = append(files, h.Sum().String())
	}
	return
}

type searchStrategy interface {
	Search(ctx context.Context, r *git.Repository, hashes []string) (closest []string, matched, total int, err error)
}

// DynamicTreeSearchStrategy  searches all TreeObjects in the repository for
// the number of matches against the input files provided.
type DynamicTreeSearchStrategy struct {
}

// Search returns the set of matching commits along with the number of matches.
func (DynamicTreeSearchStrategy) Search(ctx context.Context, r *git.Repository, hashes []string) (closest []string, matched, total int, err error) {
	files := make(map[plumbing.Hash]bool)
	for _, h := range hashes {
		_, err = r.BlobObject(plumbing.NewHash(h))
		if err == plumbing.ErrObjectNotFound {
			// Skip files not present in repo.
			err = nil
		} else if err != nil {
			return
		} else {
			files[plumbing.NewHash(h)] = true
		}
	}
	total = len(files)
	if total == 0 {
		err = errors.New("repo contains no matching files")
		return
	}
	// Construct cache of all trees and their match count.
	cache := make(map[plumbing.Hash]int)
	ti, _ := r.TreeObjects()
	ti.ForEach(func(t *object.Tree) error {
		countTree(t, files, cache)
		return nil
	})
	// Search through all commits for the one whose tree has the most matches.
	ci, _ := r.CommitObjects()
	err = ci.ForEach(func(c *object.Commit) error {
		count := cache[c.TreeHash]
		if matched < count {
			matched = count
			closest = closest[:0]
		}
		if matched == count {
			closest = append(closest, c.Hash.String())
		}
		return nil
	})
	return
}

// countTree counts the number of matching files in the given git Tree and records them in "cache".
func countTree(t *object.Tree, toMatch map[plumbing.Hash]bool, cache map[plumbing.Hash]int) (count int) {
	if val, ok := cache[t.Hash]; ok {
		return val
	}
	for _, e := range t.Entries {
		switch e.Mode {
		case filemode.Dir:
			if val, ok := cache[e.Hash]; ok {
				count += val
			} else {
				t, _ := t.Tree(e.Name)
				subcount := countTree(t, toMatch, cache)
				cache[e.Hash] = subcount
				count += subcount
			}
		case filemode.Submodule, filemode.Symlink:
			continue
		default:
			if _, ok := toMatch[e.Hash]; ok {
				count++
			}
		}
	}
	cache[t.Hash] = count
	return
}

// CommitsNearPublishStrategy searches across repository for the input files
// provided for the nearest matching commit(s).
type CommitsNearPublishStrategy struct {
	Published time.Time
	Window    time.Duration
}

// Search returns the set of matching commits along with the number of matches.
func (s CommitsNearPublishStrategy) Search(ctx context.Context, r *git.Repository, hashes []string) (closest []string, matched, total int, err error) {
	files := make(map[string]bool)
	for _, h := range hashes {
		files[h] = true
	}
	total = len(files)
	it, err := r.CommitObjects()
	if err != nil {
		return
	}
	found := make(map[string]bool)
	notNearPublishDate := func(c *object.Commit) bool {
		committed := c.Committer.When
		return committed.Before(s.Published.Add(-1*s.Window)) || committed.After(s.Published.Add(s.Window))
	}
	err = it.ForEach(func(c *object.Commit) error {
		if notNearPublishDate(c) {
			return nil
		}
		t, err := c.Tree()
		if err != nil {
			return err
		}
		clear(found)
		var count int
		tw := object.NewTreeWalker(t, true, nil)
		for {
			_, entry, err := tw.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				panic(err)
			}
			if entry.Mode == filemode.Dir || entry.Mode == filemode.Submodule {
				continue
			}
			hash := entry.Hash.String()
			if files[hash] && !found[hash] {
				count++
				found[hash] = true
			}
		}
		if matched < count {
			matched = count
			closest = closest[:0]
		}
		if matched == count {
			closest = append(closest, c.Hash.String())
		}
		return nil
	})
	return
}

// ExhaustiveTreeSearchStrategy
type ExhaustiveTreeSearchStrategy struct {
}

// Search returns the set of matching commits along with the number of matches.
func (s ExhaustiveTreeSearchStrategy) Search(ctx context.Context, r *git.Repository, hashes []string) (closest []string, matched, total int, err error) {
	// Find subset of file hashes that are present in the repo.
	files := make(map[plumbing.Hash]int)
	var i int
	for _, h := range hashes {
		_, err = r.BlobObject(plumbing.NewHash(h))
		if err == plumbing.ErrObjectNotFound {
			// Skip files not present in repo.
			err = nil
		} else if err != nil {
			return
		} else {
			files[plumbing.NewHash(h)] = i
			i++
		}
	}
	// Iterate over repo trees to create an ordering.
	trees := make(map[plumbing.Hash]int)
	i = 0
	for ti, _ := r.TreeObjects(); ; {
		t, err := ti.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		} else {
			trees[t.Hash] = i
			i++
		}
	}
	total = len(files)
	if total == 0 {
		err = errors.New("repo contains no matching files")
		return
	}
	// Recursively build set of matching files for each tree.
	matches := bitmap.NewBatch(len(files), len(trees))
	processed := bitmap.New(len(trees))
	ti, _ := r.TreeObjects()
	ti.ForEach(func(t *object.Tree) error {
		processTree(t, files, trees, matches, processed)
		return nil
	})
	// Search through all commits for the one whose tree has the most matches.
	var ci object.CommitIter
	ci, err = r.CommitObjects()
	if err != nil {
		err = errors.Wrap(err, "creating commit iterator")
		return
	}
	err = ci.ForEach(func(c *object.Commit) error {
		if !processed.Get(trees[c.TreeHash]) {
			return errors.New("unprocessed tree")
		}
		count := matches[trees[c.TreeHash]].Count()
		if matched < count {
			matched = count
			closest = closest[:0]
		}
		if matched == count {
			closest = append(closest, c.Hash.String())
		}
		return nil
	})
	return
}

// processTree recursively determines the presence of a set of files in the given git Tree and records them.
func processTree(t *object.Tree, files, trees map[plumbing.Hash]int, matches []bitmap.Bitmap, processed *bitmap.Bitmap) {
	if processed.Get(trees[t.Hash]) {
		return
	}
	for _, e := range t.Entries {
		switch e.Mode {
		case filemode.Dir:
			if !processed.Get(trees[e.Hash]) {
				st, _ := t.Tree(e.Name)
				processTree(st, files, trees, matches, processed)
			}
			matches[trees[t.Hash]].Or(&matches[trees[e.Hash]])
		case filemode.Submodule, filemode.Symlink:
			continue
		default:
			if file, ok := files[e.Hash]; ok {
				matches[trees[t.Hash]].Set(file)
			}
		}
	}
	processed.Set(trees[t.Hash])
}

func main() {
	flag.Parse()
	ctx := context.Background()
	if *repo != "" && *repoPath != "" {
		log.Fatal("-repo and -repo-path are mutually exclusive")
	}
	r, err := getRepo(ctx, *repo, *repoPath)
	if err != nil {
		log.Fatal(err)
	}
	var f io.ReadCloser
	var published time.Time
	switch *ecosystem {
	case "maven":
		p, err := mavenreg.HTTPRegistry{}.PackageVersion(ctx, *pkg, *version)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching version metadata"))
		}
		f, err = mavenreg.HTTPRegistry{}.ReleaseFile(ctx, *pkg, *version, mavenreg.TypeSources)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching source jar"))
		}
		defer f.Close()
		published = p.Published
	case "pypi":
		p, err := pypireg.HTTPRegistry{}.Project(ctx, *pkg)
		if err != nil {
			log.Fatal(errors.Wrap(err, "fetching pypi metadata"))
		}
		for _, rel := range p.Releases[*version] {
			if strings.HasSuffix(rel.Filename, "-none-any.whl") {
				resp, err := http.Get(rel.URL)
				if err != nil {
					log.Fatal(errors.Wrap(err, "requesting wheel file"))
				}
				if resp.StatusCode != 200 {
					log.Fatal(errors.Wrap(errors.New(resp.Status), "fetching wheel file"))
				}
				f = resp.Body
				defer f.Close()
				published = rel.UploadTime
				break
			}
		}
		if f == nil {
			log.Fatal("artifact not found")
		}
	default:
		log.Fatal(errors.Errorf("unknown ecosystem: %s", *ecosystem))
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(errors.Wrap(err, "reading artifact"))
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		log.Fatal(errors.Wrap(err, "creating zip reader"))
	}
	hashes, err := zipHashes(zr)
	if err != nil {
		log.Fatal(errors.Wrap(err, "hash calculation"))
	}
	var s searchStrategy
	switch *strategy {
	case "dynamic":
		s = &DynamicTreeSearchStrategy{}
	case "commits-near-publish":
		s = &CommitsNearPublishStrategy{Published: published, Window: 7 * 24 * time.Hour}
	case "dynamic-exhaustive":
		s = &ExhaustiveTreeSearchStrategy{}
	default:
		log.Fatalln("unknown strategy:", *strategy)
	}
	closest, matched, total, err := s.Search(ctx, r, hashes)
	if err != nil {
		log.Fatal(errors.Wrap(err, "identity search"))
	}
	if matched == 0 {
		log.Fatal(errors.New("no file matches"))
	}
	fmt.Printf("With matches on %d of %d files, best match: %v\n", matched, total, closest)
}
