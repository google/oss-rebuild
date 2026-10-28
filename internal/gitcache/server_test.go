// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/act/api/form"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/pkg/errors"
)

func TestGetRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     GetRequest
		wantErr bool
	}{
		{
			name:    "valid URI only",
			req:     GetRequest{URI: "github.com/org/repo"},
			wantErr: false,
		},
		{
			name:    "valid with contains",
			req:     GetRequest{URI: "github.com/org/repo", Contains: "2024-01-01T00:00:00Z"},
			wantErr: false,
		},
		{
			name:    "valid with ref",
			req:     GetRequest{URI: "github.com/org/repo", Ref: "refs/tags/v1.0"},
			wantErr: false,
		},
		{
			name:    "missing URI",
			req:     GetRequest{},
			wantErr: true,
		},
		{
			name:    "empty URI",
			req:     GetRequest{URI: ""},
			wantErr: true,
		},
		{
			name:    "bad time format",
			req:     GetRequest{URI: "github.com/org/repo", Contains: "not-a-time"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetRequestUnmarshal(t *testing.T) {
	tests := []struct {
		name         string
		values       url.Values
		wantURI      string
		wantContains string
		wantRef      string
		wantErr      bool
	}{
		{
			name:    "valid URI only",
			values:  url.Values{"uri": {"github.com/org/repo"}},
			wantURI: "github.com/org/repo",
		},
		{
			name:         "with contains and ref",
			values:       url.Values{"uri": {"github.com/org/repo"}, "contains": {"2024-01-01T00:00:00Z"}, "ref": {"refs/tags/v1.0"}},
			wantURI:      "github.com/org/repo",
			wantContains: "2024-01-01T00:00:00Z",
			wantRef:      "refs/tags/v1.0",
		},
		{
			name:   "empty values",
			values: url.Values{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req GetRequest
			err := form.Unmarshal(tt.values, &req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if req.URI != tt.wantURI {
				t.Errorf("URI = %q, want %q", req.URI, tt.wantURI)
			}
			if req.Contains != tt.wantContains {
				t.Errorf("Contains = %q, want %q", req.Contains, tt.wantContains)
			}
			if req.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", req.Ref, tt.wantRef)
			}
		})
	}
}

// writeTarGz creates a minimal gzipped tar archive containing a single file.
func writeTarGz(t *testing.T, path string, filename string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name: filename,
		Size: int64(len(data)),
		Mode: 0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerHandleGet(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		setupCache func(t *testing.T, dir string)
		wantCode   int
		wantBody   string // substring match for error cases
	}{
		{
			name:     "missing URI",
			query:    "",
			wantCode: http.StatusBadRequest,
			wantBody: "Empty URI",
		},
		{
			name:     "bad contains format",
			query:    "uri=github.com/org/repo&contains=bad-time",
			wantCode: http.StatusBadRequest,
			wantBody: "Failed to parse RFC 3339 time",
		},
		{
			name:     "future threshold",
			query:    "uri=github.com/org/repo&contains=" + url.QueryEscape(time.Now().Add(48*time.Hour).Format(time.RFC3339)),
			wantCode: http.StatusBadRequest,
			wantBody: "Time bound too far in the future",
		},
		{
			name:     "bad repo URI",
			query:    "uri=not-a-valid-repo",
			wantCode: http.StatusBadRequest,
			wantBody: "Failed to canonicalize repo URI",
		},
		{
			name:     "unsupported depth",
			query:    "uri=" + url.QueryEscape("https://example.com/a/b/c/d"),
			wantCode: http.StatusBadRequest,
			wantBody: "Unsupported repo URI",
		},
		{
			name:  "cache hit",
			query: "uri=github.com/org/repo",
			setupCache: func(t *testing.T, dir string) {
				t.Helper()
				p := filepath.Join(dir, "github.com", "org", "repo", "repo.tgz")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				writeTarGz(t, p, "HEAD", []byte("ref: refs/heads/main\n"))
			},
			wantCode: http.StatusOK,
		},
		{
			name:  "cache hit with ref",
			query: "uri=github.com/org/repo&ref=refs/tags/v1.0",
			setupCache: func(t *testing.T, dir string) {
				t.Helper()
				p := filepath.Join(dir, "github.com", "org", "repo", "refs_tags_v1.0", "repo.tgz")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				writeTarGz(t, p, "HEAD", []byte("ref: refs/tags/v1.0\n"))
			},
			wantCode: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setupCache != nil {
				tt.setupCache(t, dir)
			}
			s := &Server{backend: &localBackend{baseDir: dir}}
			req := httptest.NewRequest("GET", "/get?"+tt.query, nil)
			rr := httptest.NewRecorder()
			s.HandleGet(rr, req)
			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
			if tt.wantBody != "" {
				if body := rr.Body.String(); !strings.Contains(body, tt.wantBody) {
					t.Errorf("body = %q, want substring %q", body, tt.wantBody)
				}
			}
		})
	}
}

func TestServerHandleGetFillInProgress(t *testing.T) {
	b := &localBackend{baseDir: t.TempDir()}
	_, release, err := b.lease(context.Background(), "github.com/org/repo/repo.tgz")
	if err != nil {
		t.Fatalf("lease() error = %v, want held", err)
	}
	defer release()
	s := &Server{backend: b, cloneFunc: func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*gitx.Repo, error) {
		t.Fatal("cloned while another request holds the fill")
		return nil, nil
	}}
	rr := httptest.NewRecorder()
	s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil))
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Retry-After") != "10" {
		t.Errorf("HandleGet() = %d with Retry-After %q, want 503 with 10", rr.Code, rr.Header().Get("Retry-After"))
	}
}

// leasedBackend replaces localBackend's lease.
type leasedBackend struct {
	*localBackend
	leaseFn func(context.Context, string) (context.Context, func(), error)
}

func (b leasedBackend) lease(ctx context.Context, path string) (context.Context, func(), error) {
	return b.leaseFn(ctx, path)
}

func TestServerHandleGetLeaseFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lease func(context.Context, string) (context.Context, func(), error)
	}{
		{
			name: "lease error",
			lease: func(context.Context, string) (context.Context, func(), error) {
				return nil, nil, errors.New("storage unavailable")
			},
		},
		{
			name: "lease lost mid-fill",
			lease: func(ctx context.Context, _ string) (context.Context, func(), error) {
				held, lose := context.WithCancelCause(ctx)
				lose(errLeaseLost)
				return held, func() {}, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			p := filepath.Join(cacheDir, "github.com/org/repo/repo.tgz")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			writeTarGz(t, p, "HEAD", []byte("ref: refs/heads/main\n"))
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			stale := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(p, stale, stale); err != nil {
				t.Fatal(err)
			}
			s := &Server{backend: leasedBackend{&localBackend{baseDir: cacheDir}, tc.lease}, cloneFunc: func(ctx context.Context, _ storage.Storer, _ billy.Filesystem, _ *git.CloneOptions) (*gitx.Repo, error) {
				return nil, ctx.Err()
			}}
			rr := httptest.NewRecorder()
			s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo&contains="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil))
			if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Retry-After") == "" {
				t.Errorf("HandleGet() = %d with Retry-After %q, want 503 with one", rr.Code, rr.Header().Get("Retry-After"))
			}
			if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, want) {
				t.Errorf("entry after the request: err %v, unchanged %v, want it left for the lease holder", err, bytes.Equal(got, want))
			}
		})
	}
}

// TestServerHandleGetServeContent verifies that a cache hit serves actual tarball content.
func TestServerHandleGetServeContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "github.com", "org", "repo", "repo.tgz")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTarGz(t, p, "HEAD", []byte("ref: refs/heads/main\n"))

	s := &Server{backend: &localBackend{baseDir: dir}}
	req := httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil)
	rr := httptest.NewRecorder()
	s.HandleGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify the response is a valid gzipped tar.
	gr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next() error = %v", err)
	}
	if hdr.Name != "HEAD" {
		t.Errorf("tar entry name = %q, want %q", hdr.Name, "HEAD")
	}
	data, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "ref: refs/heads/main\n" {
		t.Errorf("tar entry content = %q, want %q", string(data), "ref: refs/heads/main\n")
	}
}

// setupLocalRepo creates a local git repo on disk for testing.
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

// localCloneFunc returns a CloneFunc that rewrites the URL to localURL
// before delegating to gitx.Clone.
func localCloneFunc(localURL string) gitx.CloneFunc {
	return func(ctx context.Context, s storage.Storer, fs billy.Filesystem, opts *git.CloneOptions) (*gitx.Repo, error) {
		opts.URL = localURL
		return gitx.Clone(ctx, s, fs, opts)
	}
}

func TestCreateTarball(t *testing.T) {
	srcDir := t.TempDir()
	// Create a known file tree.
	if err := os.MkdirAll(filepath.Join(srcDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "refs", "heads", "main"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "pack"), make([]byte, 1<<16), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := createTarball(srcDir, &buf); err != nil {
		t.Fatalf("createTarball() error = %v", err)
	}
	// Zeros compress to almost nothing, so a full-size archive was stored.
	if buf.Len() < 1<<16 {
		t.Errorf("archive of a 64 KiB pack is %d bytes, want it stored uncompressed", buf.Len())
	}

	// Decompress and verify entries.
	gr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	found := make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		if hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			found[hdr.Name] = string(data)
		}
	}

	want := map[string]string{
		"HEAD":            "ref: refs/heads/main\n",
		"refs/heads/main": "abc123\n",
		"config":          "[core]\n",
	}
	for name, wantContent := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing tar entry %q", name)
			continue
		}
		if got != wantContent {
			t.Errorf("entry %q = %q, want %q", name, got, wantContent)
		}
	}
}

func TestServerHandleGetCacheMiss(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "hello"
`
	localURL := setupLocalRepo(t, yamlRepo)
	cacheDir := t.TempDir()
	s := &Server{
		backend:   &localBackend{baseDir: cacheDir},
		cloneFunc: localCloneFunc(localURL),
	}

	req := httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil)
	rr := httptest.NewRecorder()
	s.HandleGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify the response is a valid gzip+tar containing git metadata.
	gr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var foundHEAD bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		if hdr.Name == "HEAD" {
			foundHEAD = true
		}
	}
	if !foundHEAD {
		t.Error("archive does not contain HEAD file")
	}
}

func TestServerHandleGetCacheMissWithRef(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	yamlRepo := `
commits:
  - id: initial
    branch: master
    message: "Initial commit"
    files:
      README.md: "hello"
  - id: tagged
    parent: initial
    branch: master
    message: "Tagged commit"
    tag: v1.0
    files:
      README.md: "tagged"
`
	localURL := setupLocalRepo(t, yamlRepo)
	cacheDir := t.TempDir()
	s := &Server{
		backend:   &localBackend{baseDir: cacheDir},
		cloneFunc: localCloneFunc(localURL),
	}

	req := httptest.NewRequest("GET", "/get?uri=github.com/org/repo&ref=refs/tags/v1.0", nil)
	rr := httptest.NewRecorder()
	s.HandleGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify the ref-specific cache path was created.
	refCachePath := filepath.Join(cacheDir, "github.com", "org", "repo", "refs_tags_v1.0", "repo.tgz")
	if _, err := os.Stat(refCachePath); err != nil {
		t.Errorf("expected ref-specific cache entry at %s: %v", refCachePath, err)
	}

	// Verify the response is a valid gzip+tar containing HEAD.
	gr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var foundHEAD bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next() error = %v", err)
		}
		if hdr.Name == "HEAD" {
			foundHEAD = true
		}
	}
	if !foundHEAD {
		t.Error("archive does not contain HEAD file")
	}
}

func TestSplitPacks(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	src, bare := t.TempDir(), filepath.Join(t.TempDir(), "repo.git")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", src)
	// Incompressible files, so the clone's single pack outgrows the limit.
	for i := range 4 {
		b := make([]byte, 600<<10)
		rand.Read(b)
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d", i)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("-C", src, "add", ".")
	git("-C", src, "commit", "-q", "-m", "files")
	// --no-local packs the objects, as a clone over the network does.
	git("clone", "-q", "--bare", "--no-local", src, bare)
	orig := maxPackBytes
	maxPackBytes = 1 << 20 // git's smallest pack limit
	defer func() { maxPackBytes = orig }()
	if err := splitPacks(context.Background(), bare); err != nil {
		t.Fatalf("splitPacks: %v", err)
	}
	packs, err := filepath.Glob(filepath.Join(bare, "objects", "pack", "*.pack"))
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) < 2 {
		t.Errorf("packs = %d, want the clone split into several", len(packs))
	}
	for _, p := range packs {
		if fi, err := os.Stat(p); err != nil || fi.Size() > maxPackBytes {
			t.Errorf("pack %s: %v, size over %d", p, err, maxPackBytes)
		}
	}
	git("--git-dir="+bare, "fsck", "--strict", "--no-progress")
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_DEFAULT_REF_FORMAT=files")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newUpstream makes a repository with branches main, dev and gone, a
// lightweight tag, an annotated tag, and a tag only gone reaches.
func newUpstream(t *testing.T) string {
	t.Helper()
	up := t.TempDir()
	gitRun(t, up, "init", "-q", "-b", "main")
	gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "a")
	gitRun(t, up, "tag", "v1")
	gitRun(t, up, "checkout", "-q", "-b", "dev")
	gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "b")
	gitRun(t, up, "tag", "-a", "v2", "-m", "annotated")
	gitRun(t, up, "checkout", "-q", "-b", "gone", "main")
	gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "c")
	gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "d")
	gitRun(t, up, "tag", "on-gone")
	gitRun(t, up, "checkout", "-q", "main")
	return up
}

// entryDir extracts the entry at path into a temporary directory.
func entryDir(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := archive.ExtractTar(tar.NewReader(gr), osfs.New(dir), archive.ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func entryState(t *testing.T, path string) (refs, head, objects string, loose int) {
	t.Helper()
	dir := entryDir(t, path)
	refs = gitRun(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
	head = gitRun(t, dir, "symbolic-ref", "HEAD")
	objects = gitRun(t, dir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	for _, line := range strings.Split(gitRun(t, dir, "count-objects", "-v"), "\n") {
		if n, ok := strings.CutPrefix(line, "count: "); ok {
			var err error
			if loose, err = strconv.Atoi(n); err != nil {
				t.Fatalf("count-objects: %v", err)
			}
		}
	}
	return refs, head, objects, loose
}

func TestRefreshCacheMatchesClone(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, up string)
		then   func(t *testing.T, up string) // changes upstream after one refresh
	}{
		{
			name: "new commits, branch and tag",
			change: func(t *testing.T, up string) {
				gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "e")
				gitRun(t, up, "tag", "v3")
				gitRun(t, up, "branch", "feature", "dev")
			},
		},
		{
			name: "deleted branch and tags, force-push, moved tag and new default branch",
			change: func(t *testing.T, up string) {
				// Strands dev's commit and v2's tag object, and gone's old tip.
				gitRun(t, up, "branch", "-q", "-D", "dev")
				gitRun(t, up, "tag", "-d", "v2")
				gitRun(t, up, "tag", "-d", "on-gone")
				gitRun(t, up, "checkout", "-q", "gone")
				gitRun(t, up, "reset", "-q", "--hard", "HEAD~1")
				gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "rewritten")
				gitRun(t, up, "tag", "-f", "v1", "gone")
			},
		},
		{
			// The rewrite strands the objects the first refresh fetched.
			name: "refreshed commit later force-pushed away",
			change: func(t *testing.T, up string) {
				gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "e")
			},
			then: func(t *testing.T, up string) {
				gitRun(t, up, "reset", "-q", "--hard", "HEAD~1")
				gitRun(t, up, "commit", "-q", "--allow-empty", "-m", "e2")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t)
			cacheDir := t.TempDir()
			s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: gitx.Clone, remote: func(string) string { return "file://" + up }}
			ctx := context.Background()
			if err := s.populateCache(ctx, "github.com/org/repo", "", "stale/repo.tgz"); err != nil {
				t.Fatalf("populateCache() error = %v", err)
			}
			tc.change(t, up)
			if err := s.refreshCache(ctx, "github.com/org/repo", "stale/repo.tgz"); err != nil {
				t.Fatalf("refreshCache() error = %v", err)
			}
			if tc.then != nil {
				tc.then(t, up)
				if err := s.refreshCache(ctx, "github.com/org/repo", "stale/repo.tgz"); err != nil {
					t.Fatalf("second refreshCache() error = %v", err)
				}
			}
			if err := s.populateCache(ctx, "github.com/org/repo", "", "fresh/repo.tgz"); err != nil {
				t.Fatalf("populateCache() error = %v", err)
			}
			gotRefs, gotHead, gotObjects, gotLoose := entryState(t, filepath.Join(cacheDir, "stale/repo.tgz"))
			wantRefs, wantHead, wantObjects, _ := entryState(t, filepath.Join(cacheDir, "fresh/repo.tgz"))
			if diff := cmp.Diff(wantRefs, gotRefs); diff != "" {
				t.Errorf("refreshed refs differ from a clone's (-clone +refreshed):\n%s", diff)
			}
			if gotHead != wantHead {
				t.Errorf("refreshed HEAD = %s, want %s", gotHead, wantHead)
			}
			if diff := cmp.Diff(wantObjects, gotObjects); diff != "" {
				t.Errorf("refreshed objects differ from a clone's (-clone +refreshed):\n%s", diff)
			}
			if gotLoose != 0 {
				t.Errorf("refreshed entry holds %d loose objects, want all packed", gotLoose)
			}
		})
	}
}

func TestRefreshCacheMergesPacks(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	defer func(n int) { maxPacks = n }(maxPacks)
	maxPacks = 2
	up := newUpstream(t)
	cacheDir := t.TempDir()
	s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: gitx.Clone, remote: func(string) string { return "file://" + up }}
	ctx := context.Background()
	if err := s.populateCache(ctx, "github.com/org/repo", "", "repo.tgz"); err != nil {
		t.Fatalf("populateCache() error = %v", err)
	}
	for i := range 4 {
		gitRun(t, up, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("r%d", i))
		if err := s.refreshCache(ctx, "github.com/org/repo", "repo.tgz"); err != nil {
			t.Fatalf("refreshCache() %d error = %v", i, err)
		}
		dir := entryDir(t, filepath.Join(cacheDir, "repo.tgz"))
		packs, _ := filepath.Glob(filepath.Join(dir, "objects", "pack", "*.pack"))
		if len(packs) > maxPacks {
			t.Fatalf("after refresh %d the entry holds %d packs, want at most %d", i, len(packs), maxPacks)
		}
		// A fast-forward under the limit adds its pack beside the clone's.
		if i == 0 && len(packs) != 2 {
			t.Errorf("after the first refresh the entry holds %d packs, want 2 without a repack", len(packs))
		}
		if _, err := os.Stat(filepath.Join(dir, "FETCH_HEAD")); !os.IsNotExist(err) {
			t.Errorf("refreshed entry holds FETCH_HEAD: %v", err)
		}
	}
}

func TestServerHandleGetRefreshesStaleEntryByFetch(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	up := newUpstream(t)
	cacheDir := t.TempDir()
	s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: gitx.Clone, remote: func(string) string { return "file://" + up }}
	if err := s.populateCache(context.Background(), "github.com/org/repo", "", "github.com/org/repo/repo.tgz"); err != nil {
		t.Fatalf("populateCache() error = %v", err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(cacheDir, "github.com/org/repo/repo.tgz"), stale, stale); err != nil {
		t.Fatal(err)
	}
	gitRun(t, up, "tag", "v3")
	s.cloneFunc = func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*gitx.Repo, error) {
		t.Fatal("cloned a stale entry that a fetch could refresh")
		return nil, nil
	}
	rr := httptest.NewRecorder()
	s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo&contains="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleGet() = %d: %s", rr.Code, rr.Body.String())
	}
	if refs, _, _, _ := entryState(t, filepath.Join(cacheDir, "github.com/org/repo/repo.tgz")); !strings.Contains(refs, "refs/tags/v3 ") {
		t.Errorf("refreshed entry lacks the new tag:\n%s", refs)
	}
}

func TestServerHandleGetClonesWhenRefreshFails(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	up := newUpstream(t)
	cacheDir := t.TempDir()
	p := filepath.Join(cacheDir, "github.com/org/repo/repo.tgz")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// An entry that is no repository at all cannot be fetched onto.
	writeTarGz(t, p, "README", []byte("not a repository"))
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: gitx.Clone, remote: func(string) string { return "file://" + up }}
	rr := httptest.NewRecorder()
	s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo&contains="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleGet() = %d: %s", rr.Code, rr.Body.String())
	}
	if _, head, _, _ := entryState(t, p); head != "refs/heads/main" {
		t.Errorf("entry HEAD = %s after falling back to a clone, want refs/heads/main", head)
	}
}

// stagingClone stages n bytes and waits for ctx, as an over-budget clone does.
func stagingClone(n int) gitx.CloneFunc {
	return func(ctx context.Context, st storage.Storer, _ billy.Filesystem, _ *git.CloneOptions) (*gitx.Repo, error) {
		dir := st.(*filesystem.Storage).Filesystem().Root()
		if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, n), 0o644); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("signal: killed")
		case <-time.After(5 * time.Second):
			return nil, errors.New("clone outlived its budget")
		}
	}
}

func TestFillStopsAtSizeBudget(t *testing.T) {
	defer func(d time.Duration) { sizePollInterval = d }(sizePollInterval)
	sizePollInterval = 10 * time.Millisecond
	cacheDir := t.TempDir()
	var fills int
	clone := stagingClone(2 << 20)
	s := &Server{backend: &localBackend{baseDir: cacheDir}, fillBytes: 1 << 20, cloneFunc: func(ctx context.Context, st storage.Storer, fs billy.Filesystem, o *git.CloneOptions) (*gitx.Repo, error) {
		fills++
		return clone(ctx, st, fs, o)
	}}
	for i := range 2 {
		rr := httptest.NewRecorder()
		s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil))
		if rr.Code != http.StatusRequestEntityTooLarge || rr.Header().Get(sizeLimitHeader) != strconv.Itoa(1<<20) || !strings.Contains(rr.Body.String(), "repository exceeds the size limit") {
			t.Errorf("request %d: HandleGet() = %d, limit %q, body %q, want 413 naming the 1 MiB budget", i, rr.Code, rr.Header().Get(sizeLimitHeader), rr.Body.String())
		}
	}
	// A repository over budget stays over it, so the second request fails fast.
	if fills != 1 {
		t.Errorf("fills = %d, want 1", fills)
	}
}

func TestFillStopsAtTimeBudget(t *testing.T) {
	defer func(d time.Duration) { fillTimeout = d }(fillTimeout)
	fillTimeout = 50 * time.Millisecond
	s := &Server{backend: &localBackend{baseDir: t.TempDir()}, cloneFunc: stagingClone(1)}
	rr := httptest.NewRecorder()
	s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil))
	if rr.Code != http.StatusGatewayTimeout || !strings.Contains(rr.Body.String(), ErrFillTimeout.Error()) {
		t.Errorf("HandleGet() = %d %q, want 504 naming the time budget", rr.Code, rr.Body.String())
	}
}

func TestRepeatedFailedFillsFailFast(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	cacheDir := t.TempDir()
	up := newUpstream(t)
	var fills int
	failing := true
	s := &Server{backend: &localBackend{baseDir: cacheDir}, remote: func(string) string { return "file://" + up }}
	s.cloneFunc = func(ctx context.Context, st storage.Storer, fs billy.Filesystem, o *git.CloneOptions) (*gitx.Repo, error) {
		fills++
		if failing {
			return nil, errors.New("upstream unreachable")
		}
		return gitx.Clone(ctx, st, fs, o)
	}
	get := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil))
		return rr
	}
	for i, want := range []int{500, 500, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity} {
		if rr := get(); rr.Code != want {
			t.Errorf("request %d: HandleGet() = %d %q, want %d", i, rr.Code, rr.Body.String(), want)
		}
	}
	if fills != failLimit {
		t.Errorf("fills = %d, want %d: the request after the limit must not fill", fills, failLimit)
	}
	// Once the record expires, the entry fills again, and success clears it.
	marker := filepath.Join(cacheDir, "github.com/org/repo/repo.tgz"+failuresSuffix)
	expired := time.Now().Add(-failExpiry - time.Minute)
	if err := os.Chtimes(marker, expired, expired); err != nil {
		t.Fatal(err)
	}
	failing = false
	if rr := get(); rr.Code != http.StatusOK {
		t.Fatalf("HandleGet() after expiry = %d %q, want 200", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("failure record survived a successful fill: %v", err)
	}
}

func TestFillFailuresNotCounted(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(cancel context.CancelFunc) error
	}{
		{"request ended", func(cancel context.CancelFunc) error { cancel(); return context.Canceled }},
		{"repository not found", func(context.CancelFunc) error { return errors.Wrap(transport.ErrRepositoryNotFound, "gone") }},
		{"ref not found", func(context.CancelFunc) error { return errors.Wrap(git.ErrBranchNotFound, "not yet pushed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			var fills int
			var cancel context.CancelFunc
			s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*gitx.Repo, error) {
				fills++
				return nil, tc.fail(cancel)
			}}
			for range failLimit + 1 {
				var ctx context.Context
				ctx, cancel = context.WithCancel(context.Background())
				s.HandleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/get?uri=github.com/org/repo", nil).WithContext(ctx))
				cancel()
			}
			if fills != failLimit+1 {
				t.Errorf("fills = %d, want %d: these failures must not block refills", fills, failLimit+1)
			}
			if _, err := os.Stat(filepath.Join(cacheDir, "github.com/org/repo/repo.tgz"+failuresSuffix)); !os.IsNotExist(err) {
				t.Errorf("failure record exists: %v", err)
			}
		})
	}
}

func TestRateLimitedFillsAreNotCounted(t *testing.T) {
	if !gitx.NativeGitAvailable() {
		t.Skip("native git not available")
	}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate-limited", http.StatusTooManyRequests)
	}))
	defer limited.Close()
	cacheDir := t.TempDir()
	p := filepath.Join(cacheDir, "github.com/org/repo/repo.tgz")
	var clones int
	s := &Server{backend: &localBackend{baseDir: cacheDir}, remote: func(string) string { return limited.URL + "/org/repo" }}
	s.cloneFunc = func(ctx context.Context, st storage.Storer, fs billy.Filesystem, o *git.CloneOptions) (*gitx.Repo, error) {
		clones++
		return gitx.Clone(ctx, st, fs, o)
	}
	get := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo&contains="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil))
		return rr
	}
	for i := range failLimit + 1 {
		if rr := get(); rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
			t.Errorf("request %d: HandleGet() = %d %q, want 429 with Retry-After", i, rr.Code, rr.Body.String())
		}
	}
	if _, err := os.Stat(p + failuresSuffix); !os.IsNotExist(err) {
		t.Errorf("rate-limited fills were recorded as failures: %v", err)
	}
	// A rate-limited refresh skips the clone, which the remote would refuse too.
	up := newUpstream(t)
	dir := filepath.Join(t.TempDir(), "entry.git")
	gitRun(t, up, "clone", "-q", "--bare", up, dir)
	gitRun(t, dir, "config", "remote.origin.url", limited.URL+"/org/repo")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := createTarball(dir, f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	clones = 0
	if rr := get(); rr.Code != http.StatusTooManyRequests || clones != 0 {
		t.Errorf("HandleGet() on a stale entry = %d %q after %d clones, want 429 without a clone", rr.Code, rr.Body.String(), clones)
	}
}

func TestFailedFillKeepsEntry(t *testing.T) {
	cacheDir := t.TempDir()
	p := filepath.Join(cacheDir, "github.com/org/repo/repo.tgz")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTarGz(t, p, "HEAD", []byte("ref: refs/heads/main\n"))
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	s := &Server{backend: &localBackend{baseDir: cacheDir}, cloneFunc: func(context.Context, storage.Storer, billy.Filesystem, *git.CloneOptions) (*gitx.Repo, error) {
		return nil, errors.New("upstream unreachable")
	}}
	rr := httptest.NewRecorder()
	s.HandleGet(rr, httptest.NewRequest("GET", "/get?uri=github.com/org/repo&contains="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("HandleGet() = %d, want 500", rr.Code)
	}
	// The stale entry is what a later fill refreshes from.
	if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, want) {
		t.Errorf("entry after a failed fill: err %v, unchanged %v", err, bytes.Equal(got, want))
	}
}
