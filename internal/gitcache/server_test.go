// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/act/api/form"
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
	return func(ctx context.Context, s storage.Storer, fs billy.Filesystem, opts *git.CloneOptions) (*git.Repository, error) {
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

	var buf bytes.Buffer
	if err := createTarball(srcDir, &buf); err != nil {
		t.Fatalf("createTarball() error = %v", err)
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
