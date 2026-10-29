// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitscan

import (
	"context"
	"sort"

	"github.com/google/oss-rebuild/internal/bitmap"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

// ReachableTreeCount is ExactTreeCount over go-git2, which has no object
// iterators. It scores only commits reachable from a reference, which in a
// fresh clone are all of them.
type ReachableTreeCount struct{}

// Search returns the set of matching commits along with the number of matches.
func (ReachableTreeCount) Search(ctx context.Context, r *git2.Repository, hashes []git2.Hash) (closest []string, matched, total int, err error) {
	// Find subset of file hashes that are present in the repo.
	// NOTE: Indexes files as ExactTreeCount does, so results match it.
	files := make(map[git2.Hash]int)
	var i int
	for _, h := range hashes {
		if _, ok := files[h]; ok {
			continue // a repeated blob counts once
		}
		ok, err := r.HasObject(h)
		if err != nil {
			return nil, 0, 0, err
		}
		if ok {
			files[h] = i
			i++
		}
	}
	total = len(files)
	if total == 0 {
		return nil, 0, 0, errors.New("repo contains no matching files")
	}
	s := treeScorer{r: r, files: files, scores: make(map[git2.Hash]*bitmap.Bitmap)}
	commits, err := r.Log(&git2.LogOptions{All: true})
	if err != nil {
		return nil, 0, 0, errors.Wrap(err, "walking commits")
	}
	err = commits.ForEach(func(c *git2.Commit) error {
		m, err := s.score(c.TreeHash)
		if err != nil {
			return errors.Wrapf(err, "scoring tree of %s", c.Hash)
		}
		count := m.Count()
		if matched < count {
			matched = count
			closest = closest[:0]
		}
		if matched == count && matched > 0 {
			closest = append(closest, c.Hash.String())
		}
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	sort.Strings(closest)
	return closest, matched, total, nil
}

// treeScorer records which of files each tree contains, reading each tree once.
type treeScorer struct {
	r      *git2.Repository
	files  map[git2.Hash]int
	scores map[git2.Hash]*bitmap.Bitmap
}

func (s *treeScorer) score(h git2.Hash) (*bitmap.Bitmap, error) {
	if m, ok := s.scores[h]; ok {
		return m, nil
	}
	t, err := s.r.TreeObject(h)
	if err != nil {
		return nil, err
	}
	m := bitmap.New(len(s.files))
	for _, e := range t.Entries {
		switch e.Mode {
		case git2.Dir:
			sub, err := s.score(e.Hash)
			if err != nil {
				return nil, err
			}
			m.Or(sub)
		case git2.Submodule, git2.Symlink:
			continue
		default:
			if file, ok := s.files[e.Hash]; ok {
				m.Set(file)
			}
		}
	}
	s.scores[h] = m
	return m, nil
}
