// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"context"
	"testing"

	"io"
	"os/exec"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

// setupLocalRepo creates a local git repo on disk for testing with native git.
// Returns the file:// URL to the repo.
func setupLocalRepo(t *testing.T, yamlSpec string) string {
	t.Helper()
	upstreamDir := t.TempDir()
	upstreamFS := osfs.New(upstreamDir)
	_, err := gitxtest.CreateRepoFromYAML(yamlSpec, &gitxtest.RepositoryOptions{
		Storer:   filesystem.NewStorage(upstreamFS, cache.NewObjectLRUDefault()),
		Worktree: upstreamFS,
	})
	if err != nil {
		t.Fatalf("failed to create test repo: %v", err)
	}
	return "file://" + upstreamDir
}

func TestNativeClone_UnsupportedOptions(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}
	ctx := context.Background()
	storer := memory.NewStorage()
	tests := []struct {
		name string
		opts *git.CloneOptions
	}{
		{
			name: "Auth",
			opts: &git.CloneOptions{
				URL:  "file:///nonexistent",
				Auth: &http.BasicAuth{Username: "user", Password: "pass"},
			},
		},
		{
			name: "RemoteName",
			opts: &git.CloneOptions{
				URL:        "file:///nonexistent",
				RemoteName: "upstream",
			},
		},
		{
			name: "Tags",
			opts: &git.CloneOptions{
				URL:  "file:///nonexistent",
				Tags: git.AllTags,
			},
		},
		{
			name: "InsecureSkipTLS",
			opts: &git.CloneOptions{
				URL:             "file:///nonexistent",
				InsecureSkipTLS: true,
			},
		},
		{
			name: "CABundle",
			opts: &git.CloneOptions{
				URL:      "file:///nonexistent",
				CABundle: []byte("cert"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NativeClone(ctx, storer, nil, tc.opts)
			if err == nil {
				t.Errorf("expected error for unsupported option %s, got nil", tc.name)
			}
		})
	}
}

func TestNativeClone_Storer(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}
	ctx := context.Background()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "Test content"
  - id: second
    parent: initial
    branch: master
    message: "Second commit"
    files:
      README.md: "Updated content"
`
	upstreamURL := setupLocalRepo(t, yamlRepo)
	tests := []struct {
		name   string
		storer func(t *testing.T) storage.Storer
	}{
		{
			name: "filesystem_osfs",
			storer: func(t *testing.T) storage.Storer {
				return filesystem.NewStorage(osfs.New(t.TempDir()), cache.NewObjectLRUDefault())
			},
		},
		{
			name: "filesystem_memfs",
			storer: func(t *testing.T) storage.Storer {
				return filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault())
			},
		},
		{
			name: "memory",
			storer: func(t *testing.T) storage.Storer {
				return memory.NewStorage()
			},
		},
		{
			name: "custom",
			storer: func(t *testing.T) storage.Storer {
				return &struct{ *memory.Storage }{memory.NewStorage()}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storer := tc.storer(t)
			repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
				URL:        upstreamURL,
				NoCheckout: true,
			})
			if err != nil {
				t.Fatalf("NativeClone failed: %v", err)
			}
			// Verify the returned repo uses the same storer we passed in
			if repo.Storer != storer {
				t.Error("repo.Storer is not the same instance as the passed-in storer")
			}
			// Verify HEAD exists and points to second commit
			head, err := repo.Head()
			if err != nil {
				t.Fatalf("failed to get HEAD: %v", err)
			}
			commit, err := repo.CommitObject(head.Hash())
			if err != nil {
				t.Fatalf("failed to get commit: %v", err)
			}
			if commit.Message != "Second commit" {
				t.Errorf("unexpected commit message: %q", commit.Message)
			}
			// Verify we got the expected commit graph
			commitCount := 0
			iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
			if err != nil {
				t.Fatalf("failed to get log: %v", err)
			}
			err = iter.ForEach(func(c *object.Commit) error {
				commitCount++
				return nil
			})
			if err != nil {
				t.Fatalf("failed to iterate commits: %v", err)
			}
			if commitCount != 2 {
				t.Errorf("expected 2 commits, got %d", commitCount)
			}
			// Verify config has remote
			cfg, err := repo.Config()
			if err != nil {
				t.Fatalf("failed to get config: %v", err)
			}
			if _, ok := cfg.Remotes["origin"]; !ok {
				t.Error("origin remote not found in config")
			}
		})
	}
}

func TestNativeClone_RemoteTrackingRefs(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}
	ctx := context.Background()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial on master"
    files:
      README.md: "master"
  - id: feature
    parent: initial
    branch: feature-branch
    message: "Feature commit"
    files:
      feature.txt: "feature"
  - id: dev
    parent: initial
    branch: develop
    message: "Dev commit"
    files:
      dev.txt: "develop"
`
	upstreamURL := setupLocalRepo(t, yamlRepo)
	storer := memory.NewStorage()
	repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
		URL:        upstreamURL,
		NoCheckout: true,
	})
	if err != nil {
		t.Fatalf("NativeClone failed: %v", err)
	}
	// Verify remote tracking refs exist
	expectedRefs := []string{
		"refs/remotes/origin/master",
		"refs/remotes/origin/feature-branch",
		"refs/remotes/origin/develop",
	}
	for _, refName := range expectedRefs {
		ref, err := repo.Reference(plumbing.ReferenceName(refName), false)
		if err != nil {
			t.Errorf("failed to get reference %s: %v", refName, err)
			continue
		}
		if ref.Hash().IsZero() {
			t.Errorf("reference %s has zero hash", refName)
		}
	}
	// Verify we can list all references
	refs, err := repo.References()
	if err != nil {
		t.Fatalf("failed to list references: %v", err)
	}
	refCount := 0
	err = refs.ForEach(func(r *plumbing.Reference) error {
		refCount++
		return nil
	})
	if err != nil {
		t.Fatalf("failed to iterate references: %v", err)
	}
	// Should have HEAD + local branches + remote tracking refs
	if refCount == 9 {
		t.Errorf("expected 9 references (HEAD + 4 local + 4 remote tracking refs), got %d", refCount)
	}
}

func TestNativeClone_CloneOptions(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}
	ctx := context.Background()
	yamlRepo := `
commits:
  - id: c1
    branch: master
    message: "Commit 1"
    files:
      file.txt: "v1"
  - id: c2
    parent: c1
    branch: master
    message: "Commit 2"
    files:
      file.txt: "v2"
  - id: c3
    parent: c2
    branch: master
    message: "Commit 3"
    files:
      file.txt: "v3"
  - id: f1
    parent: c1
    branch: feature
    message: "Feature commit"
    files:
      feature.txt: "feature"
`
	upstreamURL := setupLocalRepo(t, yamlRepo)
	t.Run("Depth", func(t *testing.T) {
		storer := memory.NewStorage()
		repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
			URL:        upstreamURL,
			Depth:      1,
			NoCheckout: true,
		})
		if err != nil {
			t.Fatalf("NativeClone failed: %v", err)
		}
		// With depth=1, we should only get the tip commit
		head, _ := repo.Head()
		commitCount := 0
		iter, _ := repo.Log(&git.LogOptions{From: head.Hash()})
		iter.ForEach(func(c *object.Commit) error {
			commitCount++
			return nil
		})
		if commitCount != 1 {
			t.Errorf("expected 1 commit with depth=1, got %d", commitCount)
		}
	})

	t.Run("SingleBranch", func(t *testing.T) {
		storer := memory.NewStorage()
		repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
			URL:           upstreamURL,
			ReferenceName: plumbing.Master,
			SingleBranch:  true,
			NoCheckout:    true,
		})
		if err != nil {
			t.Fatalf("NativeClone failed: %v", err)
		}
		// With single branch, we should only have master tracking ref
		_, err = repo.Reference("refs/remotes/origin/master", false)
		if err != nil {
			t.Errorf("expected master tracking ref: %v", err)
		}
		// Feature branch should not exist
		_, err = repo.Reference("refs/remotes/origin/feature", false)
		if err == nil {
			t.Error("expected feature branch to not exist with SingleBranch")
		}
	})

	t.Run("ReferenceName", func(t *testing.T) {
		storer := memory.NewStorage()
		repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
			URL:           upstreamURL,
			ReferenceName: plumbing.NewBranchReferenceName("feature"),
			SingleBranch:  true,
			NoCheckout:    true,
		})
		if err != nil {
			t.Fatalf("NativeClone failed: %v", err)
		}
		// HEAD should point to feature branch
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("failed to get HEAD: %v", err)
		}
		commit, _ := repo.CommitObject(head.Hash())
		if commit.Message != "Feature commit" {
			t.Errorf("expected feature commit, got: %q", commit.Message)
		}
	})

	t.Run("Tag", func(t *testing.T) {
		// Create a repo with a tag to test tag-specific clone.
		tagYamlRepo := `
commits:
  - id: c1
    branch: master
    message: "Commit 1"
    files:
      file.txt: "v1"
  - id: c2
    parent: c1
    branch: master
    message: "Commit 2"
    tag: v2.0
    files:
      file.txt: "v2"
  - id: c3
    parent: c2
    branch: master
    message: "Commit 3"
    files:
      file.txt: "v3"
`
		tagUpstreamURL := setupLocalRepo(t, tagYamlRepo)
		storer := memory.NewStorage()
		repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
			URL:           tagUpstreamURL,
			ReferenceName: plumbing.NewTagReferenceName("v2.0"),
			SingleBranch:  true,
			NoCheckout:    true,
		})
		if err != nil {
			t.Fatalf("NativeClone failed: %v", err)
		}
		// HEAD should point to the tagged commit
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("failed to get HEAD: %v", err)
		}
		commit, err := repo.CommitObject(head.Hash())
		if err != nil {
			t.Fatalf("failed to get commit: %v", err)
		}
		if commit.Message != "Commit 2" {
			t.Errorf("expected tagged commit message %q, got: %q", "Commit 2", commit.Message)
		}
		// Tag ref should exist
		_, err = repo.Reference("refs/tags/v2.0", false)
		if err != nil {
			t.Errorf("expected tag ref refs/tags/v2.0: %v", err)
		}
	})
}

func TestNativeClone_Worktree(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}

	ctx := context.Background()
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "Hello"
      src/main.go: "package main"
`
	upstreamURL := setupLocalRepo(t, yamlRepo)
	// Clone with worktree (checkout)
	// Storage and worktree need separate directories
	targetDir := t.TempDir()
	gitDir := targetDir + "/.git"
	if err := osfs.New(targetDir).MkdirAll(".git", 0755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
	gitFS := osfs.New(gitDir)
	wtFS := osfs.New(targetDir)
	storer := filesystem.NewStorage(gitFS, cache.NewObjectLRUDefault())
	repo, err := NativeClone(ctx, storer, wtFS, &git.CloneOptions{
		URL:        upstreamURL,
		NoCheckout: false, // Enable checkout
	})
	if err != nil {
		t.Fatalf("NativeClone failed: %v", err)
	}
	// Verify worktree has files
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	// Check README.md exists
	f, err := wt.Filesystem.Open("README.md")
	if err != nil {
		t.Fatalf("failed to open README.md: %v", err)
	}
	f.Close()
	// Check src/main.go exists
	f, err = wt.Filesystem.Open("src/main.go")
	if err != nil {
		t.Fatalf("failed to open src/main.go: %v", err)
	}
	f.Close()
}

func TestNativeClone_FetchAfterClone(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}

	ctx := context.Background()

	// Create upstream repo
	upstreamDir := t.TempDir()
	upstreamFS := osfs.New(upstreamDir)
	upstreamStorer := filesystem.NewStorage(upstreamFS, cache.NewObjectLRUDefault())

	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "v1"
`
	upstreamRepo, err := gitxtest.CreateRepoFromYAML(yamlRepo, &gitxtest.RepositoryOptions{
		Storer:   upstreamStorer,
		Worktree: upstreamFS,
	})
	if err != nil {
		t.Fatalf("failed to create upstream repo: %v", err)
	}

	// Clone the repo
	storer := memory.NewStorage()
	repo, err := NativeClone(ctx, storer, nil, &git.CloneOptions{
		URL:        "file://" + upstreamDir,
		NoCheckout: true,
	})
	if err != nil {
		t.Fatalf("NativeClone failed: %v", err)
	}

	// Get initial HEAD
	initialHead, _ := repo.Head()

	// Add a new commit to upstream
	wt, _ := upstreamRepo.Worktree()
	f, _ := wt.Filesystem.Create("new-file.txt")
	f.Write([]byte("new content"))
	f.Close()
	wt.Add("new-file.txt")
	newCommitHash, err := wt.Commit("New commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test"},
	})
	if err != nil {
		t.Fatalf("failed to create new commit: %v", err)
	}

	// Fetch from upstream
	err = repo.FetchContext(ctx, &git.FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Verify new commit is available
	_, err = repo.CommitObject(newCommitHash)
	if err != nil {
		t.Errorf("new commit not found after fetch: %v", err)
	}

	// Verify tracking ref is updated
	originMaster, err := repo.Reference("refs/remotes/origin/master", false)
	if err != nil {
		t.Fatalf("failed to get origin/master: %v", err)
	}
	if originMaster.Hash() == initialHead.Hash() {
		t.Error("origin/master was not updated after fetch")
	}
	if originMaster.Hash() != newCommitHash {
		t.Errorf("origin/master hash mismatch: expected %s, got %s", newCommitHash, originMaster.Hash())
	}
}

func TestNativeClone_Submodules(t *testing.T) {
	if !NativeGitAvailable() {
		t.Skip("native git not available")
	}
	ctx := context.Background()

	// Create a "submodule" repo on disk.
	subDir := t.TempDir()
	subFS := osfs.New(subDir)
	_, err := gitxtest.CreateRepoFromYAML(`
commits:
  - id: initial
    branch: master
    message: "Sub initial"
    files:
      sub.txt: "submodule content"
`, &gitxtest.RepositoryOptions{
		Storer:   filesystem.NewStorage(subFS, cache.NewObjectLRUDefault()),
		Worktree: subFS,
	})
	if err != nil {
		t.Fatalf("failed to create submodule repo: %v", err)
	}

	// Create a "main" repo using native git so we can use `git submodule add`.
	mainDir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "master")
	run("-c", "protocol.file.allow=always", "submodule", "add", "file://"+subDir, "mysub")
	run("commit", "-m", "add submodule")

	// Clone with RecurseSubmodules.
	targetDir := t.TempDir()
	gitDir := targetDir + "/.git"
	if err := osfs.New(targetDir).MkdirAll(".git", 0755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
	storer := filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault())
	wtFS := osfs.New(targetDir)

	repo, err := NativeClone(ctx, storer, wtFS, &git.CloneOptions{
		URL:               "file://" + mainDir,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
	})
	if err != nil {
		t.Fatalf("NativeClone failed: %v", err)
	}

	// Verify the submodule file exists in the worktree.
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	f, err := wt.Filesystem.Open("mysub/sub.txt")
	if err != nil {
		t.Fatalf("submodule file mysub/sub.txt not found: %v", err)
	}
	content, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		t.Fatalf("failed to read submodule file: %v", err)
	}
	if string(content) != "submodule content" {
		t.Errorf("unexpected submodule file content: %q", string(content))
	}
}

func TestUpdateSubmodules_NoSubmodules(t *testing.T) {
	// Create a simple repo with no submodules.
	storer := memory.NewStorage()
	fs := memfs.New()
	repo, err := gitxtest.CreateRepoFromYAML(`
commits:
  - id: initial
    branch: master
    message: "Initial"
    files:
      README.md: "hello"
`, &gitxtest.RepositoryOptions{
		Storer:   storer,
		Worktree: fs,
	})
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	// UpdateSubmodules should return nil when there are no submodules.
	if err := UpdateSubmodules(context.Background(), repo.Repository, git.DefaultSubmoduleRecursionDepth); err != nil {
		t.Errorf("UpdateSubmodules returned error on repo without submodules: %v", err)
	}
}
