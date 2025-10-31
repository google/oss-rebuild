// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitscan

import (
	"archive/zip"
	"context"
	"io"
	"sort"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/bitmap"
	"github.com/pkg/errors"
)

// BlobHashesFromZip computes the git blob hashes for all files in the provided zip archive.
func BlobHashesFromZip(zr *zip.Reader) (files []plumbing.Hash, err error) {
	var f io.ReadCloser
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		size := int64(zf.UncompressedSize64)
		if size < 0 {
			// TODO: support git LFS?
			return nil, errors.Errorf("file exceeds max supported size: %d", zf.UncompressedSize64)
		}
		h := plumbing.NewHasher(plumbing.BlobObject, size)
		f, err = zf.Open()
		if err != nil {
			return nil, err
		}
		if _, err = io.CopyN(h, f, size); err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		files = append(files, h.Sum())
	}
	return files, nil
}

// LazyTreeCount searches all TreeObjects in the repository for the number of matches against the input files provided.
type LazyTreeCount struct{}

// Search returns the set of matching commits along with the number of matches.
func (c LazyTreeCount) Search(ctx context.Context, r *git.Repository, hashes []plumbing.Hash) (closest []string, matched, total int, err error) {
	files := make(map[plumbing.Hash]bool)
	for _, h := range hashes {
		_, err = r.BlobObject(h)
		if err == plumbing.ErrObjectNotFound {
			// Skip files not present in repo.
			err = nil
		} else if err != nil {
			return
		} else {
			files[h] = true
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
	sort.Strings(closest)
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

// CommitsNearPublish searches across repository for the input files provided for the nearest matching commit(s).
type CommitsNearPublish struct {
	Published time.Time
	Window    time.Duration
}

// Search returns the set of matching commits along with the number of matches.
func (s CommitsNearPublish) Search(ctx context.Context, r *git.Repository, hashes []plumbing.Hash) (closest []string, matched, total int, err error) {
	files := make(map[plumbing.Hash]bool)
	for _, h := range hashes {
		files[h] = true
	}
	total = len(files)
	it, err := r.CommitObjects()
	if err != nil {
		return
	}
	found := make(map[plumbing.Hash]bool)
	notNearPublishDate := func(c *object.Commit) bool {
		committed := c.Committer.When
		return committed.Before(s.Published.Add(-1*s.Window)) || committed.After(s.Published.Add(s.Window))
	}
	// TODO: consider using time bounded log as it might be more efficient.
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
			hash := entry.Hash
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
	sort.Strings(closest)
	return
}

// ExactTreeCount searches all TreeObjects in the repository to match the set of input files provided.
type ExactTreeCount struct{}

// Search returns the set of matching commits along with the number of matches.
func (s ExactTreeCount) Search(ctx context.Context, r *git.Repository, hashes []plumbing.Hash) (closest []string, matched, total int, err error) {
	// Find subset of file hashes that are present in the repo.
	files := make(map[plumbing.Hash]int)
	var i int
	for _, h := range hashes {
		_, err = r.BlobObject(h)
		if err == plumbing.ErrObjectNotFound {
			// Skip files not present in repo.
			err = nil
		} else if err != nil {
			return
		} else {
			files[h] = i
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
	sort.Strings(closest)
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
