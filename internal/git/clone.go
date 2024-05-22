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

// Package git provides rebuilder-specific git abstractions.
package git

import (
	"context"
	"os/exec"

	billy "github.com/go-git/go-billy/v5"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/pkg/errors"
)

// CloneFunc defines an interface for cloning a git repo.
type CloneFunc func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*git.Repository, error)

// Clone performs a normal clone operation.
var Clone CloneFunc = git.CloneContext

var (
	// ErrRemoteNotTracked is returned when a reuse is attempted but the remote does not match.
	ErrRemoteNotTracked = errors.New("existing repository does not track desired remote")
)

// Reuse reuses the existing git repo in Storer and Filesystem.
func Reuse(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*git.Repository, error) {
	if opt.Auth != nil || opt.RemoteName != "" || opt.ReferenceName != "" || opt.SingleBranch || opt.Depth != 0 || opt.Tags != git.InvalidTagMode || opt.InsecureSkipTLS || len(opt.CABundle) > 0 {
		// No support for non-trivial opts aside from NoCheckout.
		return nil, errors.New("Unsupported opt")
	}
	u, err := uri.CanonicalizeRepoURI(opt.URL)
	if err != nil {
		return nil, err
	}
	repo, err := git.Open(s, fs)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	var match bool
	for _, originURL := range cfg.Remotes[git.DefaultRemoteName].URLs {
		ou, err := uri.CanonicalizeRepoURI(originURL)
		if err != nil {
			continue
		}
		match = match || (ou == u)
	}
	if !match {
		return nil, ErrRemoteNotTracked
	}
	wt, err := repo.Worktree()
	switch err {
	case git.ErrIsBareRepository:
		return nil, errors.New("Cannot use reuse bare repository")
	case nil:
		if opt.NoCheckout {
			return nil, errors.New("Cannot convert non-bare to bare repository")
		}
	default:
		return nil, err
	}
	// Reset any local changes and checkout master.
	// TODO: Replace this with a call to wt.Clean() once the All flag is supported.
	// https://github.com/go-git/go-git/pull/995
	{
		cmd := exec.CommandContext(ctx, "git", "clean", "-ffdx")
		cmd.Dir = fs.Root()
		if err := cmd.Run(); err != nil {
			return nil, errors.Wrap(err, "cleaning repo")
		}
	}
	// TODO: master may not be origin/master.
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.Master, Force: true})
	if err == plumbing.ErrReferenceNotFound {
		// Try main if master failed.
		if err := wt.Checkout(&git.CheckoutOptions{Branch: "refs/heads/main", Force: true}); err != nil {
			return nil, errors.Wrapf(err, "Failed to checkout")
		}
	} else if err != nil {
		return nil, errors.Wrapf(err, "Failed to checkout")
	}
	return repo, nil
}

var _ CloneFunc = Reuse
