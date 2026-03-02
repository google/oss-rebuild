// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package git provides rebuilder-specific git abstractions.
package gitx

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/pkg/errors"
)

// CloneFunc defines an interface for cloning a git repo.
type CloneFunc func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*git.Repository, error)

// Clone performs a clone operation, using native git if available,
// otherwise falling back to go-git.
func Clone(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*git.Repository, error) {
	if NativeGitAvailable() {
		switch s.(type) {
		case *filesystem.Storage:
			log.Println("Found git binary. Cloning using git")
			return NativeClone(ctx, s, fs, opt)
		case *memory.Storage:
			// NOTE: While supported, this can range from 2x to 5x slower with great penalties for larger repos.
			log.Println("Found git binary but using memory.Storage. Cloning using go-git")
			return git.CloneContext(ctx, s, fs, opt)
		default:
			log.Printf("Found git binary but using unknown Storer %T. Cloning using go-git", s)
			return git.CloneContext(ctx, s, fs, opt)
		}
	}
	log.Println("No git binary found. Cloning using go-git")
	return git.CloneContext(ctx, s, fs, opt)
}

var _ CloneFunc = Clone

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
		if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.Main, Force: true}); err != nil {
			return nil, errors.Wrapf(err, "Failed to checkout")
		}
	} else if err != nil {
		return nil, errors.Wrapf(err, "Failed to checkout")
	}
	return repo, nil
}

var _ CloneFunc = Reuse

var (
	nativeGitAvailable     bool
	nativeGitAvailableOnce sync.Once
)

// NativeGitAvailable returns true if the native git command is available in PATH.
func NativeGitAvailable() bool {
	nativeGitAvailableOnce.Do(func() {
		_, err := exec.LookPath("git")
		nativeGitAvailable = err == nil
	})
	return nativeGitAvailable
}

// isOSFilesystem checks if a billy.Filesystem is backed by the real OS filesystem.
// Frustratingly, there's really no type assertion that works for this so we do so
// by creating a temp file via billy and verifying it's reachable via os.Stat.
func isOSFilesystem(bfs billy.Filesystem) bool {
	f, err := bfs.TempFile("", ".os-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	defer bfs.Remove(name)
	_, err = os.Stat(filepath.Join(bfs.Root(), name))
	return err == nil
}

// NativeClone clones a git repository using the native `git` command.
// If the target storage is not OS-backed, the results are first staged on disk.
// Supports both filesystem.Storage and memory.Storage.
func NativeClone(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*git.Repository, error) {
	if opt.Auth != nil {
		return nil, errors.New("unsupported clone option for native git: Auth")
	}
	if opt.RemoteName != "" {
		return nil, errors.Errorf("unsupported clone option for native git: RemoteName=%s", opt.RemoteName)
	}
	if opt.Tags != git.InvalidTagMode {
		return nil, errors.Errorf("unsupported clone option for native git: Tags=%v", opt.Tags)
	}
	if opt.InsecureSkipTLS {
		return nil, errors.New("unsupported clone option for native git: InsecureSkipTLS")
	}
	if len(opt.CABundle) > 0 {
		return nil, errors.New("unsupported clone option for native git: CABundle")
	}
	// Determine storage type and whether staging is needed
	var targetDir string
	var needsStaging bool
	if sf, ok := s.(*filesystem.Storage); ok && isOSFilesystem(sf.Filesystem()) {
		// We can clone directly into the target dir for osfs-based fs storers.
		targetDir = sf.Filesystem().Root()
	} else {
		needsStaging = true
		var err error
		targetDir, err = os.MkdirTemp("", "native-git-clone-*")
		if err != nil {
			return nil, errors.Wrap(err, "creating staging directory")
		}
		defer os.RemoveAll(targetDir)
		log.Printf("NativeClone: using staging directory for %T", s)
	}
	// Build git clone command
	// NOTE: Always do bare clone. When needed, do checkout with go-git
	args := []string{"clone", "--bare"}
	if opt.Depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", opt.Depth))
	}
	if opt.SingleBranch {
		args = append(args, "--single-branch")
	}
	if opt.ReferenceName != "" {
		args = append(args, "--branch", opt.ReferenceName.Short())
	}
	//
	// NOTE: This replicates the refspec logic implemented in go-git's Repository.cloneRefSpec.
	remoteName := cmp.Or(opt.RemoteName, git.DefaultRemoteName)
	var fetchRefSpec string
	switch {
	case opt.ReferenceName.IsTag():
		// Tags are pulled by default and the other refspecs are incompatible with the tag ref
	case opt.SingleBranch && opt.ReferenceName == plumbing.HEAD:
		fetchRefSpec = fmt.Sprintf("+HEAD:refs/remotes/%s/HEAD", remoteName)
	case opt.SingleBranch:
		fetchRefSpec = fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%[1]s", opt.ReferenceName.Short(), remoteName)
	default:
		fetchRefSpec = fmt.Sprintf("+refs/heads/*:refs/remotes/%s/*", remoteName)
	}
	if fetchRefSpec != "" {
		args = append(args, "-c", "remote."+remoteName+".fetch="+fetchRefSpec)
	}
	args = append(args, opt.URL, targetDir)
	// Execute the git command
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrapf(err, "native git clone failed: %s", string(output))
	}
	// Copy staging to target storage if needed
	if needsStaging {
		stagingFS := osfs.New(targetDir)
		stagingStorer := filesystem.NewStorage(stagingFS, cache.NewObjectLRUDefault())
		if sf, ok := s.(*filesystem.Storage); ok {
			if err := billyx.CopyFS(sf.Filesystem(), stagingFS); err != nil {
				return nil, errors.Wrap(err, "copying from staging to storage filesystem")
			}
		} else {
			if err := CopyStorer(s, stagingStorer); err != nil {
				return nil, errors.Wrap(err, "copying from staging to memory storage")
			}
		}
	}
	// Open the repository with go-git using the passed-in storage
	repo, err := git.Open(s, fs)
	if err != nil {
		return nil, errors.Wrap(err, "opening cloned repository")
	}
	// If worktree requested and not NoCheckout, do checkout with go-git
	if fs != nil && !opt.NoCheckout {
		wt, err := repo.Worktree()
		if err != nil {
			return nil, errors.Wrap(err, "getting worktree")
		}
		checkoutOpts := &git.CheckoutOptions{}
		if opt.ReferenceName != "" {
			checkoutOpts.Branch = opt.ReferenceName
		}
		if err := wt.Checkout(checkoutOpts); err != nil {
			return nil, errors.Wrap(err, "checking out worktree")
		}
	}
	// If submodules requested, init and update them
	if fs != nil && !opt.NoCheckout && opt.RecurseSubmodules != git.NoRecurseSubmodules {
		if err := UpdateSubmodules(ctx, repo, opt.RecurseSubmodules); err != nil {
			return nil, errors.Wrap(err, "updating submodules")
		}
	}
	return repo, nil
}

var _ CloneFunc = NativeClone

// UpdateSubmodules initializes and updates submodules for the given repository.
// If the repository has no submodules, this is a no-op.
func UpdateSubmodules(ctx context.Context, repo *git.Repository, recurse git.SubmoduleRescursivity) error {
	wt, err := repo.Worktree()
	if err != nil {
		return errors.Wrap(err, "getting worktree")
	}
	subs, err := wt.Submodules()
	if err != nil {
		return errors.Wrap(err, "reading submodules")
	}
	if len(subs) == 0 {
		return nil
	}
	return subs.UpdateContext(ctx, &git.SubmoduleUpdateOptions{
		Init:              true,
		RecurseSubmodules: recurse,
	})
}
