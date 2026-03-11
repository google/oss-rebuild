// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

const testRepoYAML = `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "hello world"
`

// setupCloneTestServer creates a test HTTP server that mimics the gitcache
// protocol (302 redirect to tarball URL) and returns a configured Client.
func setupCloneTestServer(t *testing.T, yamlSpec string) Client {
	t.Helper()
	// Create a repo on disk with git metadata in a .git subdirectory.
	// Client.Clone uses ExtractTar with SubDir=".git", so the tarball
	// entries must be prefixed with ".git/".
	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, git.GitDirName)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
	if _, err := gitxtest.CreateRepoFromYAML(yamlSpec, &gitxtest.RepositoryOptions{
		Storer:   filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault()),
		Worktree: osfs.New(repoDir),
	}); err != nil {
		t.Fatalf("failed to create test repo: %v", err)
	}
	// Remove the index to match production behavior (bare clone has no index).
	os.Remove(filepath.Join(gitDir, "index"))
	var tarball bytes.Buffer
	if err := createTarball(repoDir, &tarball); err != nil {
		t.Fatalf("failed to create tarball: %v", err)
	}
	tarballBytes := tarball.Bytes()
	// Set up a server that implements the two-step gitcache protocol:
	//   /get → 302 redirect to /tarball (absolute URL)
	//   /tarball → serve the .tgz content
	mux := http.NewServeMux()
	// We need to start the server first to know its URL for the redirect.
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ts.URL+"/tarball", http.StatusFound)
	})
	mux.HandleFunc("/tarball", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(tarballBytes)
	})
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	return Client{
		IDClient:  ts.Client(),
		APIClient: ts.Client(),
		URL:       u,
	}
}

func TestClientClone(t *testing.T) {
	tests := []struct {
		name       string
		storer     func(t *testing.T) storage.Storer
		worktree   func(t *testing.T) billy.Filesystem
		noCheckout bool
	}{
		{
			name: "filesystem storage",
			storer: func(t *testing.T) storage.Storer {
				dir := t.TempDir()
				return filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())
			},
			worktree: func(t *testing.T) billy.Filesystem {
				return osfs.New(t.TempDir())
			},
			noCheckout: false,
		},
		{
			name: "memory storage",
			storer: func(t *testing.T) storage.Storer {
				return memory.NewStorage()
			},
			worktree: func(t *testing.T) billy.Filesystem {
				return memfs.New()
			},
			noCheckout: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := setupCloneTestServer(t, testRepoYAML)
			s := tc.storer(t)
			fs := tc.worktree(t)
			repo, err := client.Clone(context.Background(), s, fs, &git.CloneOptions{
				URL:        "https://github.com/org/repo",
				NoCheckout: tc.noCheckout,
			})
			if err != nil {
				t.Fatalf("Clone() error = %v", err)
			}
			// Verify HEAD is resolvable.
			head, err := repo.Head()
			if err != nil {
				t.Fatalf("Head() error = %v", err)
			}
			if head.Hash().IsZero() {
				t.Error("Head() returned zero hash")
			}
			// Verify commit history is accessible.
			iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
			if err != nil {
				t.Fatalf("Log() error = %v", err)
			}
			commit, err := iter.Next()
			if err != nil {
				t.Fatalf("Log().Next() error = %v", err)
			}
			if commit.Message != "Initial commit" {
				t.Errorf("commit message = %q, want %q", commit.Message, "Initial commit")
			}
			// For checkout case, verify worktree file exists.
			if !tc.noCheckout {
				f, err := fs.Open("README.md")
				if err != nil {
					t.Fatalf("failed to open README.md in worktree: %v", err)
				}
				f.Close()
			}
		})
	}
}
