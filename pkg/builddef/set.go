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

package builddef

import (
	"context"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// BuildDefinitionSet represents a collection of build definitions.
type BuildDefinitionSet interface {
	Get(ctx context.Context, target rebuild.Target) (rebuild.Strategy, error)
}

// FilesystemBuildDefinitionSet implements BuildDefinitionSet using a filesystem.
type FilesystemBuildDefinitionSet struct {
	fs billy.Filesystem
}

func NewFilesystemBuildDefinitionSet(fs billy.Filesystem) *FilesystemBuildDefinitionSet {
	return &FilesystemBuildDefinitionSet{fs: fs}
}

func (s *FilesystemBuildDefinitionSet) Get(ctx context.Context, t rebuild.Target) (rebuild.Strategy, error) {
	definitions := rebuild.NewFilesystemAssetStore(s.fs)
	r, _, err := definitions.Reader(ctx, rebuild.Asset{Type: rebuild.BuildDef, Target: t})
	if err != nil {
		if errors.Is(err, rebuild.ErrAssetNotFound) {
			return nil, nil // Return nil strategy if definition is not found
		}
		return nil, errors.Wrap(err, "reading build definition")
	}
	defer r.Close()
	var strategy rebuild.Strategy
	if err := yaml.NewDecoder(r).Decode(strategy); err != nil {
		return nil, errors.Wrap(err, "parsing build definition")
	}
	return strategy, nil
}

func (s *FilesystemBuildDefinitionSet) Path(ctx context.Context, t rebuild.Target) (string, error) {
	definitions := rebuild.NewFilesystemAssetStore(s.fs)
	_, pth, err := definitions.Reader(ctx, rebuild.Asset{Type: rebuild.BuildDef, Target: t})
	return pth, err
}

type GitBuildDefinitionSet struct {
	fs  billy.Filesystem
	ref plumbing.Hash
}

// GitBuildDefinitionSetOptions provides configuration options for creating a BuildDefinitionSet from a Git repository.
type GitBuildDefinitionSetOptions struct {
	git.CloneOptions
	RelativePath       string
	SparseCheckoutDirs []string
}

// NewBuildDefinitionSetFromGit creates a BuildDefinitionSet from a new Git repository.
func NewBuildDefinitionSetFromGit(opts *GitBuildDefinitionSetOptions) (*GitBuildDefinitionSet, error) {
	if opts.ReferenceName.String() == "" {
		opts.ReferenceName = plumbing.Main
	}
	mfs := memfs.New()
	r, err := git.Clone(memory.NewStorage(), mfs, &opts.CloneOptions)
	if err != nil {
		return nil, errors.Wrap(err, "cloning repository")
	}
	w, err := r.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "getting worktree")
	}
	ref, err := r.Reference(opts.ReferenceName, true)
	if err != nil {
		return nil, errors.Wrap(err, "resolving ReferenceName")
	}
	err = w.Checkout(&git.CheckoutOptions{
		Branch:                    opts.ReferenceName,
		SparseCheckoutDirectories: opts.SparseCheckoutDirs,
	})
	if err != nil {
		return nil, errors.Wrap(err, "git checkout")
	}
	defnfs, err := mfs.Chroot(opts.RelativePath)
	if err != nil {
		return nil, errors.Wrap(err, "making relative path")
	}
	return &GitBuildDefinitionSet{fs: defnfs, ref: ref.Hash()}, nil
}

func (s *GitBuildDefinitionSet) Get(ctx context.Context, t rebuild.Target) (rebuild.Strategy, error) {
	return (&FilesystemBuildDefinitionSet{fs: s.fs}).Get(ctx, t)
}

func (s *GitBuildDefinitionSet) Path(ctx context.Context, t rebuild.Target) (string, error) {
	return (&FilesystemBuildDefinitionSet{fs: s.fs}).Path(ctx, t)
}

func (s *GitBuildDefinitionSet) Ref() plumbing.Hash {
	return s.ref
}
