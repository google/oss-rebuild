// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitxtest

import (
	"bytes"
	"io"
	"path"
	"slices"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

type FileContent map[string]string

type Commit struct {
	ID      string      `yaml:"id"`
	Message string      `yaml:"message"`
	Author  string      `yaml:"author,omitempty"`
	Parent  string      `yaml:"parent,omitempty"`
	Parents []string    `yaml:"parents,omitempty"`
	Branch  string      `yaml:"branch.omitempty"`
	Tag     string      `yaml:"tag,omitempty"`
	Tags    []string    `yaml:"tags,omitempty"`
	Files   FileContent `yaml:"files"`
}

type GitHistory struct {
	Commits []Commit `yaml:"commits"`
}

type Repository struct {
	*git.Repository
	Commits map[string]plumbing.Hash
}

type RepositoryOptions struct {
	Storer   storage.Storer
	Worktree billy.Filesystem
}

func CreateRepoFromYAML(content string, opts *RepositoryOptions) (*Repository, error) {
	var history GitHistory
	d := yaml.NewDecoder(bytes.NewReader([]byte(content)))
	d.KnownFields(true) // Fail on unknown fields
	if err := d.Decode(&history); err != nil {
		return nil, err
	}
	return CreateRepo(history.Commits, opts)
}

func CreateRepo(commits []Commit, opts *RepositoryOptions) (*Repository, error) {
	var repo Repository
	var err error
	// Create a new repository in memory
	var s storage.Storer
	if opts != nil && opts.Storer != nil {
		s = opts.Storer
	} else {
		s = memory.NewStorage()
	}
	var wfs billy.Filesystem
	if opts != nil && opts.Worktree != nil {
		wfs = opts.Worktree
	} else {
		wfs = memfs.New()
	}
	repo.Repository, err = git.Init(s, wfs)
	if err != nil {
		return nil, errors.Wrap(err, "initializing repo")
	}

	w, err := repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "accessing worktree")
	}

	// Map to store created commits
	repo.Commits = make(map[string]plumbing.Hash)

	for _, c := range commits {
		// Create or update files
		err = createFiles(w, c.Files)
		if err != nil {
			return nil, errors.Wrap(err, "creating files")
		}

		// Get parent commits
		var parents []plumbing.Hash
		if len(c.Parents) > 0 {
			for _, parentID := range c.Parents {
				parents = append(parents, repo.Commits[parentID])
			}
		} else if c.Parent != "" {
			parents = append(parents, repo.Commits[c.Parent])
		}

		// Create commit
		author := "Place Holder"
		if c.Author != "" {
			author = c.Author
		}
		commitHash, err := w.Commit(c.Message, &git.CommitOptions{
			Author:            &object.Signature{Name: author},
			AllowEmptyCommits: true,
			Parents:           parents,
		})
		if err != nil {
			return nil, errors.Wrap(err, "getting commit")
		}

		repo.Commits[c.ID] = commitHash

		// Create or update branch
		if c.Branch != "" {
			if _, err := repo.Branch(c.Branch); err == git.ErrBranchNotFound {
				if err := repo.CreateBranch(&config.Branch{Name: c.Branch}); err != nil {
					return nil, errors.Wrap(err, "creating branch")
				}
			} else if err != nil {
				return nil, errors.Wrap(err, "getting branch")
			}
			err = repo.Storer.SetReference(
				plumbing.NewHashReference(plumbing.NewBranchReferenceName(c.Branch), commitHash))
			if err != nil {
				return nil, errors.Wrap(err, "setting branch")
			}
		}

		// Create tags
		tags := slices.Clone(c.Tags)
		if c.Tag != "" {
			tags = append(tags, c.Tag)
		}
		for _, tagName := range tags {
			_, err := repo.CreateTag(tagName, commitHash, nil)
			if err != nil {
				return nil, errors.Wrap(err, "create tags")
			}
		}
	}

	return &repo, nil
}

func createFiles(w *git.Worktree, files FileContent) error {
	for name, content := range files {
		// Ensure the directory exists
		dir := path.Dir(name)
		err := w.Filesystem.MkdirAll(dir, 0755)
		if err != nil {
			return err
		}

		// Create and write to the file
		f, err := w.Filesystem.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, content)
		if err != nil {
			f.Close()
			return err
		}
		err = f.Close()
		if err != nil {
			return err
		}

		// Stage the file
		_, err = w.Add(name)
		if err != nil {
			return err
		}
	}
	return nil
}
