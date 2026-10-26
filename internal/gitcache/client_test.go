// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

// bigContent exceeds go-git's 16KiB small-object threshold, so packfile reads
// will access it lazily via the backed storer.
var bigContent = strings.Repeat("x", 64*1024)

// entryFetchTime is the Last-Modified the fixture cache serves its tarball
// with, standing in for the entry's upstream fetch time.
var entryFetchTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

var testRepoYAML = `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "hello world"
      big.bin: "` + bigContent + `"
`

// setupCloneTestServer creates a test HTTP server that mimics the gitcache
// protocol (302 redirect to tarball URL) and returns a configured Client.
// The tarball is produced by the server's populateCache so its layout (git
// dir contents at the archive root) matches production.
func setupCloneTestServer(t *testing.T, yamlSpec string) Client {
	t.Helper()
	// Stand in for the network clone: build the repo from YAML directly into
	// the storer populateCache provides.
	cloneFunc := func(ctx context.Context, s storage.Storer, fs billy.Filesystem, opt *git.CloneOptions) (*gitx.Repo, error) {
		repo, err := gitxtest.CreateRepoFromYAML(yamlSpec, &gitxtest.RepositoryOptions{
			Storer:   s,
			Worktree: memfs.New(),
		})
		if err != nil {
			return nil, err
		}
		// Repack loose objects into a packfile to match production tarballs
		// (cached repos are packed, and only packed objects are read lazily).
		if err := repo.RepackObjects(&git.RepackConfig{}); err != nil {
			return nil, err
		}
		// Remove the index to match production behavior (bare clone has no index).
		if sf, ok := s.(*filesystem.Storage); ok {
			sf.Filesystem().Remove("index")
		}
		return &gitx.Repo{Repository: repo.Repository}, nil
	}
	cacheDir := t.TempDir()
	srv := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: cloneFunc}
	if err := srv.populateCache(context.Background(), "github.com/org/repo", "", "repo.tgz"); err != nil {
		t.Fatalf("failed to populate cache: %v", err)
	}
	tarballBytes, err := os.ReadFile(filepath.Join(cacheDir, "repo.tgz"))
	if err != nil {
		t.Fatalf("failed to read cached tarball: %v", err)
	}
	// Set up a server that implements the two-step gitcache protocol:
	//   /get → 302 redirect to /tarball (absolute URL)
	//   /tarball → serve the .tgz content
	mux := http.NewServeMux()
	// We need to start the server first to know its URL for the redirect.
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sizeHeader, strconv.Itoa(len(tarballBytes)))
		http.Redirect(w, r, ts.URL+"/tarball", http.StatusFound)
	})
	mux.HandleFunc("/tarball", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Last-Modified", entryFetchTime.Format(http.TimeFormat))
		// Storage declares the length, else a large body goes out chunked.
		w.Header().Set("Content-Length", strconv.Itoa(len(tarballBytes)))
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
			if !repo.FetchedAt.Equal(entryFetchTime) {
				t.Errorf("FetchedAt = %v, want %v", repo.FetchedAt, entryFetchTime)
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
			// Verify a blob above go-git's small-object threshold is readable
			// after Clone returns to ensure staged storer remains accessible.
			f, err := commit.File("big.bin")
			if err != nil {
				t.Fatalf("File(big.bin) error = %v", err)
			}
			contents, err := f.Contents()
			if err != nil {
				t.Fatalf("Contents() error = %v", err)
			}
			if len(contents) != len(bigContent) {
				t.Errorf("big.bin length = %d, want %d", len(contents), len(bigContent))
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

func TestClientCloneRefusesOversize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared bool // response carries Content-Length
	}{
		{"DeclaredLength", true},
		{"UndeclaredLength", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := setupCloneTestServer(t, testRepoYAML)
			var size int64
			api := *client.APIClient
			base := api.Transport
			api.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(r)
				if err == nil {
					size = resp.ContentLength
					if !tc.declared {
						resp.ContentLength = -1
					}
				}
				return resp, err
			})
			client.APIClient = &api
			// The fixture gzips down to a few hundred bytes, so the budget sits
			// below anything that could hold a packfile.
			client.MaxBytes = 64
			_, err := client.Clone(context.Background(), gitx.NewInMemoryStorer(), memfs.New(), &git.CloneOptions{URL: "https://github.com/org/repo", NoCheckout: true})
			var tooLarge *gitx.RepoTooLargeError
			if !errors.As(err, &tooLarge) {
				t.Fatalf("Clone() error = %v, want a RepoTooLargeError", err)
			}
			want := gitx.RepoTooLargeError{URL: "https://github.com/org/repo", Limit: 64}
			if tc.declared {
				want.Size = size
			}
			if *tooLarge != want {
				t.Errorf("RepoTooLargeError = %+v, want %+v", *tooLarge, want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// filling answers the first n requests as in-progress fills.
func filling(n int, retryAfter string, base http.RoundTripper, calls *int) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if *calls++; *calls <= n {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {retryAfter}}, Body: http.NoBody, Request: r}, nil
		}
		return base.RoundTrip(r)
	})
}

func TestClientSize(t *testing.T) {
	const repo = "https://github.com/org/repo"
	client := setupCloneTestServer(t, testRepoYAML)
	link, err := client.GetLink(context.Background(), repo, time.Time{})
	if err != nil {
		t.Fatalf("GetLink() error = %v", err)
	}
	resp, err := client.APIClient.Get(link)
	if err != nil {
		t.Fatalf("fetching the archive: %v", err)
	}
	resp.Body.Close()
	want := resp.ContentLength
	if got, err := client.Size(context.Background(), repo); err != nil || got != want {
		t.Fatalf("Size() = %d, %v, want %d", got, err, want)
	}
	client.MaxBytes = want - 1
	_, err = client.Size(context.Background(), repo)
	var tooLarge *gitx.RepoTooLargeError
	if !errors.As(err, &tooLarge) || *tooLarge != (gitx.RepoTooLargeError{URL: repo, Limit: want - 1, Size: want}) {
		t.Errorf("Size() over MaxBytes: err = %v, want a RepoTooLargeError carrying %d", err, want)
	}
}

func TestClientCloneWaitsForFill(t *testing.T) {
	client := setupCloneTestServer(t, testRepoYAML)
	var calls int
	idc := *client.IDClient
	idc.Transport = filling(2, "0", client.IDClient.Transport, &calls)
	client.IDClient = &idc
	if _, err := client.Clone(context.Background(), gitx.NewInMemoryStorer(), memfs.New(), &git.CloneOptions{URL: "https://github.com/org/repo", NoCheckout: true}); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if calls != 3 {
		t.Errorf("cache requests = %d, want 3 (two answered as filling)", calls)
	}
}

func TestClientCloneStopsWaitingWithContext(t *testing.T) {
	client := setupCloneTestServer(t, testRepoYAML)
	var calls int
	idc := *client.IDClient
	idc.Transport = filling(1<<30, "60", client.IDClient.Transport, &calls)
	client.IDClient = &idc
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Clone(ctx, gitx.NewInMemoryStorer(), memfs.New(), &git.CloneOptions{URL: "https://github.com/org/repo", NoCheckout: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Clone() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("cache requests = %d, want 1", calls)
	}
}

func TestClientSizeStopsWithContextMidRequest(t *testing.T) {
	// The request that leads a fill blocks until the fill ends.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c := Client{IDClient: ts.Client(), APIClient: ts.Client(), URL: u}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Size(ctx, "github.com/org/repo"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Size() error = %v, want context.DeadlineExceeded", err)
	}
}
